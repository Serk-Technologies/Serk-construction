// Cash-flow statement, direct method (CAP-01.5.b; migration 0091).
package kernel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// CashFlowStatement reports the movements of CASH accounts in [From, To],
// attributed to the class of the other accounts of each journal.
type CashFlowStatement struct {
	EntityID       string          `json:"entity_id"`
	From           string          `json:"from"`
	To             string          `json:"to"`
	CashAccounts   int             `json:"cash_accounts"`
	Operating      []ReportLine    `json:"operating"`
	Investing      []ReportLine    `json:"investing"`
	Financing      []ReportLine    `json:"financing"`
	TotalOperating decimal.Decimal `json:"total_operating"`
	TotalInvesting decimal.Decimal `json:"total_investing"`
	TotalFinancing decimal.Decimal `json:"total_financing"`
	NetChange      decimal.Decimal `json:"net_change"`
	OpeningCash    decimal.Decimal `json:"opening_cash"`
	ClosingCash    decimal.Decimal `json:"closing_cash"`
	// Difference = closing - (opening + net change); zero when the statement reconciles.
	Difference decimal.Decimal `json:"difference"`
	Reconciles bool            `json:"reconciles"`
}

// CashFlow returns the cash-flow statement of an entity for [from, to].
func (s *Service) CashFlow(ctx context.Context, p auth.Principal, entityID string, from, to time.Time) (CashFlowStatement, error) {
	out := CashFlowStatement{EntityID: entityID, From: from.Format("2006-01-02"), To: to.Format("2006-01-02"),
		Operating: []ReportLine{}, Investing: []ReportLine{}, Financing: []ReportLine{}}
	if to.Before(from) {
		return out, fmt.Errorf("%w: the period ends before it starts", ErrInvalidInput)
	}
	if err := s.reportScope(p, auth.PermJournalRead); err != nil {
		return out, err
	}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE legal_entity_id = $1::uuid AND account_cash_flow_class(id) = 'CASH'`, entityID).Scan(&out.CashAccounts); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT class, account_id::text, code, name, amount FROM cash_flow_lines($1::uuid, $2::date, $3::date)`, entityID, from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var class string
			var l ReportLine
			if err := rows.Scan(&class, &l.AccountID, &l.AccountCode, &l.AccountName, &l.Amount); err != nil {
				return err
			}
			switch class {
			case "INVESTING":
				out.Investing, out.TotalInvesting = append(out.Investing, l), out.TotalInvesting.Add(l.Amount)
			case "FINANCING":
				out.Financing, out.TotalFinancing = append(out.Financing, l), out.TotalFinancing.Add(l.Amount)
			default:
				out.Operating, out.TotalOperating = append(out.Operating, l), out.TotalOperating.Add(l.Amount)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT cash_balance($1::uuid, $2::date - 1), cash_balance($1::uuid, $3::date)`, entityID, from, to).Scan(&out.OpeningCash, &out.ClosingCash)
	})
	out.NetChange = out.TotalOperating.Add(out.TotalInvesting).Add(out.TotalFinancing)
	out.Difference = out.ClosingCash.Sub(out.OpeningCash.Add(out.NetChange))
	out.Reconciles = out.Difference.IsZero()
	return out, err
}

// ProposeCashFlowClass proposes the class of an account (account.manage); it
// applies once someone else approves it.
func (s *Service) ProposeCashFlowClass(ctx context.Context, p auth.Principal, accountID, class string) (string, error) {
	class = strings.ToUpper(strings.TrimSpace(class))
	if !oneOf(class, "CASH", "OPERATING", "INVESTING", "FINANCING") {
		return "", fmt.Errorf("%w: the class is CASH, OPERATING, INVESTING or FINANCING", ErrInvalidInput)
	}
	if err := s.authz.Require(p, auth.PermAccountManage, p.TenantID); err != nil {
		return "", err
	}
	var id string
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO cash_flow_classes (account_id, class, created_by) VALUES ($1::uuid, $2, $3) RETURNING id::text`, accountID, class, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "cash_flow_class.propose", "cash_flow_class", id, "cash_flow_class.proposed", map[string]any{"id": id, "account_id": accountID, "class": class})
	})
	return id, err
}

// ApproveCashFlowClass approves a proposed class (journal.approve, not its proposer; attested).
func (s *Service) ApproveCashFlowClass(ctx context.Context, p auth.Principal, id string) error {
	return s.approveDesignation(ctx, p, auth.PermJournalApprove, "cash_flow_class", "cash_flow_classes", id,
		func(ctx context.Context, p auth.Principal, perm auth.Permission, fn func(context.Context, pgx.Tx) error) error {
			if err := s.authz.Require(p, perm, p.TenantID); err != nil {
				return err
			}
			return s.store.WithTenant(ctx, p.TenantID, fn)
		})
}

// CashFlowClasses lists an entity's accounts with the class that applies and any pending proposal.
func (s *Service) CashFlowClasses(ctx context.Context, p auth.Principal, entityID string) ([]map[string]any, error) {
	if err := s.reportScope(p, auth.PermJournalRead); err != nil {
		return nil, err
	}
	var out []map[string]any
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = rowsAsMaps(ctx, tx, `SELECT a.id::text AS account_id, a.code, a.name, a.type, account_cash_flow_class(a.id) AS class,
			EXISTS (SELECT 1 FROM cash_flow_classes c WHERE c.account_id = a.id AND c.status = 'APPROVED') AS approved,
			(SELECT row_to_json(x) FROM (SELECT c.id, c.class, c.created_by FROM cash_flow_classes c WHERE c.account_id = a.id AND c.status = 'PENDING') x) AS pending
			FROM accounts a WHERE a.legal_entity_id = $1::uuid ORDER BY a.code`, entityID)
		return err
	})
	return out, err
}
