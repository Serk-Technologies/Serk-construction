// Contract notices and claims (WP-2.6; SRS CON-04, CON-06).
//
// A notice records an event that may entitle the contractor to time or money
// and the date by which the contract requires it to be notified; the database
// computes that date. A claim follows: prepared by one person, approved for
// sending to the client by another (fresh MFA), after which the client's
// answer is recorded once. A claim whose notice was missing or late needs a
// stated reason. Migration: 0046_claims.sql.
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

// NoticeInput raises a contract notice.
type NoticeInput struct {
	ContractID string `json:"contract_id"`
	ClauseRef  string `json:"clause_ref"`
	Subject    string `json:"subject"`
	EventDate  string `json:"event_date"`
	DueDays    int    `json:"due_days"` // 0 = 28
}

// ClaimInput prepares a claim.
type ClaimInput struct {
	ContractID   string          `json:"contract_id"`
	NoticeID     string          `json:"notice_id,omitempty"`
	Kind         string          `json:"kind"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Amount       decimal.Decimal `json:"amount"`
	Days         int             `json:"days"`
	WaiverReason string          `json:"waiver_reason,omitempty"`
}

// ClaimOutcome is the client's answer.
type ClaimOutcome struct {
	Outcome      string          `json:"outcome"` // AGREED | REJECTED | DISPUTED
	Reference    string          `json:"client_reference"`
	AgreedAmount decimal.Decimal `json:"agreed_amount"`
	AgreedDays   int             `json:"agreed_days"`
	On           string          `json:"outcome_on"`
}

// Notice is a notice with its computed standing.
type Notice struct {
	ID         string `json:"id"`
	Number     string `json:"number"`
	ContractID string `json:"contract_id"`
	ClauseRef  string `json:"clause_ref"`
	Subject    string `json:"subject"`
	EventDate  string `json:"event_date"`
	DueOn      string `json:"due_on"`
	SentOn     string `json:"sent_on,omitempty"`
	Standing   string `json:"standing"` // OPEN | DUE_SOON | OVERDUE | SENT | SENT_LATE
	DaysLeft   int    `json:"days_left"`
}

// CreateNotice raises a notice against an active contract.
func (s *Service) CreateNotice(ctx context.Context, p auth.Principal, in NoticeInput) (string, error) {
	var id string
	err := s.claimTx(ctx, p, in.ContractID, func(ctx context.Context, tx pgx.Tx) error {
		ev, err := time.Parse("2006-01-02", strings.TrimSpace(in.EventDate))
		if err != nil {
			return fmt.Errorf("%w: event_date must be a date (YYYY-MM-DD)", ErrInvalidInput)
		}
		if in.DueDays == 0 {
			in.DueDays = 28
		}
		num, err := postgres.NextNumber(ctx, tx, "NTC")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO contract_notices (contract_id, number, clause_ref, subject, event_date, due_days, created_by)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7) RETURNING id::text`, in.ContractID, num, strings.TrimSpace(in.ClauseRef), strings.TrimSpace(in.Subject), ev, in.DueDays, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "notice.create", "contract_notice", id, "notice.created", map[string]any{"notice_id": id, "contract_id": in.ContractID, "number": num})
	})
	return id, err
}

// claimTx checks claim.manage on the contract's project, then runs fn in a tenant transaction.
func (s *Service) claimTx(ctx context.Context, p auth.Principal, contractID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if err := s.authz.Require(p, auth.PermClaimManage, p.TenantID); err != nil {
		return err
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var project string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM contracts WHERE id = $1::uuid`, contractID).Scan(&project); err != nil {
			return mapNoRows(err)
		}
		if err := s.authz.RequireProject(p, auth.PermClaimManage, p.TenantID, project); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// SendNotice records that the notice was sent on the given date (once).
func (s *Service) SendNotice(ctx context.Context, p auth.Principal, id, sentOn string) error {
	on, err := time.Parse("2006-01-02", strings.TrimSpace(sentOn))
	if err != nil {
		return fmt.Errorf("%w: sent_on must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	var contract string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT contract_id::text FROM contract_notices WHERE id = $1::uuid`, id).Scan(&contract)
	})
	if err != nil {
		return mapNoRows(err)
	}
	return s.claimTx(ctx, p, contract, func(ctx context.Context, tx pgx.Tx) error {
		if err := postgres.Exec1(ctx, tx, `UPDATE contract_notices SET status = 'SENT', sent_on = $2, sent_by = $3 WHERE id = $1::uuid AND status = 'OPEN'`, id, on, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "notice.send", "contract_notice", id, "notice.sent", map[string]any{"notice_id": id, "sent_on": sentOn})
	})
}

// ListNotices returns the notices of the principal's projects with their standing.
func (s *Service) ListNotices(ctx context.Context, p auth.Principal, contractID string) ([]Notice, error) {
	out := []Notice{}
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		sc := s.authz.ScopeOf(p)
		rows, err := tx.Query(ctx, `SELECT n.id::text, n.number, n.contract_id::text, n.clause_ref, n.subject, n.event_date::text, n.due_on::text, COALESCE(n.sent_on::text, ''),
			CASE WHEN n.sent_on IS NOT NULL AND n.sent_on <= n.due_on THEN 'SENT' WHEN n.sent_on IS NOT NULL THEN 'SENT_LATE'
			     WHEN current_date > n.due_on THEN 'OVERDUE' WHEN n.due_on - current_date <= 7 THEN 'DUE_SOON' ELSE 'OPEN' END,
			(n.due_on - current_date)
			FROM contract_notices n JOIN contracts c ON c.id = n.contract_id
			WHERE ($1 = '' OR n.contract_id = $1::uuid) AND ($2 OR c.project_id = ANY($3::uuid[])) ORDER BY n.due_on`, contractID, sc.TenantWide, sc.Projects)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n Notice
			if err := rows.Scan(&n.ID, &n.Number, &n.ContractID, &n.ClauseRef, &n.Subject, &n.EventDate, &n.DueOn, &n.SentOn, &n.Standing, &n.DaysLeft); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}

// CreateClaim prepares a DRAFT claim.
func (s *Service) CreateClaim(ctx context.Context, p auth.Principal, in ClaimInput) (string, error) {
	in.Kind = strings.ToUpper(strings.TrimSpace(in.Kind))
	switch in.Kind {
	case "TIME", "COST", "BOTH":
	default:
		return "", fmt.Errorf("%w: kind must be TIME, COST or BOTH", ErrInvalidInput)
	}
	var id string
	err := s.claimTx(ctx, p, in.ContractID, func(ctx context.Context, tx pgx.Tx) error {
		num, err := postgres.NextNumber(ctx, tx, "CLM")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO contract_claims (contract_id, notice_id, number, kind, title, description, amount, days, waiver_reason, created_by)
			VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7, $8, NULLIF($9, ''), $10) RETURNING id::text`,
			in.ContractID, in.NoticeID, num, in.Kind, strings.TrimSpace(in.Title), strings.TrimSpace(in.Description), in.Amount, in.Days, strings.TrimSpace(in.WaiverReason), p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "claim.create", "contract_claim", id, "claim.created", map[string]any{"claim_id": id, "contract_id": in.ContractID, "number": num})
	})
	return id, err
}

// SubmitClaim submits a draft claim for approval (by its author).
func (s *Service) SubmitClaim(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermClaimManage, "contract_claim", "contract_claims", id, "claim.submit", "claim.submitted")
}

// DecideClaim approves a claim for sending to the client, or rejects it (fresh MFA, not the author).
func (s *Service) DecideClaim(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermClaimApprove, "contract_claim", "contract_claims", id, "claim.decide", "claim", approve, reason, nil)
}

// RecordClaimOutcome records the client's answer to an approved claim (once).
func (s *Service) RecordClaimOutcome(ctx context.Context, p auth.Principal, id string, o ClaimOutcome) error {
	o.Outcome = strings.ToUpper(strings.TrimSpace(o.Outcome))
	switch o.Outcome {
	case "AGREED", "REJECTED", "DISPUTED":
	default:
		return fmt.Errorf("%w: outcome must be AGREED, REJECTED or DISPUTED", ErrInvalidInput)
	}
	on, err := time.Parse("2006-01-02", strings.TrimSpace(o.On))
	if err != nil {
		return fmt.Errorf("%w: outcome_on must be a date (YYYY-MM-DD)", ErrInvalidInput)
	}
	if len(strings.TrimSpace(o.Reference)) < 2 {
		return fmt.Errorf("%w: the client's reference is required", ErrInvalidInput)
	}
	if o.Outcome != "AGREED" {
		o.AgreedAmount, o.AgreedDays = decimal.Zero, 0
	}
	var contract string
	if err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT contract_id::text FROM contract_claims WHERE id = $1::uuid`, id).Scan(&contract)
	}); err != nil {
		return mapNoRows(err)
	}
	return s.claimTx(ctx, p, contract, func(ctx context.Context, tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM contract_claims WHERE id = $1::uuid FOR UPDATE`, id).Scan(&status); err != nil {
			return mapNoRows(err)
		}
		if status != "APPROVED" {
			return fmt.Errorf("%w: the client's answer is recorded only on an approved claim (this one is %s)", ErrInvalidInput, status)
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE contract_claims SET client_outcome = $2, client_reference = $3, agreed_amount = $4, agreed_days = $5, outcome_on = $6, outcome_by = $7
			WHERE id = $1::uuid AND client_outcome IS NULL`, id, o.Outcome, strings.TrimSpace(o.Reference), o.AgreedAmount, o.AgreedDays, on, p.UserID); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "claim.outcome", "contract_claim", id, "claim.outcome_recorded", map[string]any{"claim_id": id, "outcome": o.Outcome})
	})
}
