// Site returns: unused material issued to a project goes back to a store at
// the issue's cost (SUP-05.4, second part; migration 0089).
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

// StockReturnInput returns part of an approved stock issue to a store.
type StockReturnInput struct {
	IssueID     string          `json:"issue_id"`
	WarehouseID string          `json:"warehouse_id"`
	Quantity    decimal.Decimal `json:"quantity"`
	ReturnDate  time.Time       `json:"-"`
	Reason      string          `json:"reason"`
}

// CreateStockReturn prepares a DRAFT return (stock.issue on the issue's project).
// Item, project, cost code and cost account are taken from the issue.
func (s *Service) CreateStockReturn(ctx context.Context, p auth.Principal, in StockReturnInput) (string, error) {
	if err := stockDate(in.ReturnDate); err != nil {
		return "", err
	}
	if len(strings.TrimSpace(in.Reason)) < 5 {
		return "", fmt.Errorf("%w: say why the material comes back", ErrInvalidInput)
	}
	if !in.Quantity.IsPositive() {
		return "", fmt.Errorf("%w: the quantity returned is positive", ErrInvalidInput)
	}
	var id string
	err := s.p2pTx(ctx, p, auth.PermStockIssue, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.loadDoc(ctx, tx, p, auth.PermStockIssue, "stock_issue", in.IssueID); err != nil {
			return err
		}
		if _, err := s.visibleDoc(ctx, tx, p, "warehouse", in.WarehouseID, false); err != nil {
			return err
		}
		num, err := postgres.NextNumber(ctx, tx, "SRT")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO stock_returns (legal_entity_id, number, issue_id, warehouse_id, stock_item_id, project_id, cost_code_id,
			expense_account_id, quantity, return_date, reason, currency, created_by)
			SELECT i.legal_entity_id, $1, i.id, $3::uuid, i.stock_item_id, i.project_id, i.cost_code_id, i.expense_account_id, $4, $5, $6, i.currency, $7
			  FROM stock_issues i WHERE i.id = $2::uuid RETURNING id::text`,
			num, in.IssueID, in.WarehouseID, in.Quantity, in.ReturnDate, strings.TrimSpace(in.Reason), p.UserID)
		if err != nil {
			return mapNoRows(err)
		}
		return s.record(ctx, tx, p, "stock.return.create", "stock_return", id, "stock.return_prepared", map[string]any{"stock_return_id": id, "number": num, "issue_id": in.IssueID})
	})
	return id, err
}

// ApproveStockReturn approves a DRAFT return (stock.issue.approve, not its
// preparer): the database puts the stock back at the issue's cost and the
// STK_RETURN journal is generated in the same transaction.
func (s *Service) ApproveStockReturn(ctx context.Context, p auth.Principal, id string) (string, error) {
	var jid string
	err := s.p2pTx(ctx, p, auth.PermStockIssueApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermStockIssueApprove, "stock_return", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "stock return"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "stock.return.approve", "stock_return", id, "APPROVED"); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE stock_returns SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid`, id, p.UserID); err != nil {
			return err
		}
		jid, err = s.stockJournal(ctx, tx, p, "STK_RETURN", "stock_returns", "return_date", id)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "stock.return.approve", "stock_return", id, "stock.returned", map[string]any{"stock_return_id": id, "journal_id": jid})
	})
	return jid, err
}

// RejectStockReturn (approver) or CancelStockReturn (preparer) closes a DRAFT return with a reason.
func (s *Service) RejectStockReturn(ctx context.Context, p auth.Principal, id, reason string) error {
	return s.closeStockReturn(ctx, p, auth.PermStockIssueApprove, id, "REJECTED", "stock.return.reject", reason)
}

// CancelStockReturn withdraws the preparer's own DRAFT return.
func (s *Service) CancelStockReturn(ctx context.Context, p auth.Principal, id, reason string) error {
	return s.closeStockReturn(ctx, p, auth.PermStockIssue, id, "CANCELLED", "stock.return.cancel", reason)
}

func (s *Service) closeStockReturn(ctx context.Context, p auth.Principal, perm auth.Permission, id, next, action, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	return s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, perm, "stock_return", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if next == "CANCELLED" && d.CreatedBy != p.UserID {
			return fmt.Errorf("%w: only the preparer cancels a return", auth.ErrForbidden)
		}
		if next == "REJECTED" {
			if err := auth.CheckMakerChecker(p, d.CreatedBy, "stock return"); err != nil {
				return err
			}
		}
		if err := s.authorize(ctx, tx, p, action, "stock_return", id, next); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE stock_returns SET status = $2, decision_reason = $3 WHERE id = $1::uuid`, id, next, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, "stock_return", id, "stock.return_"+strings.ToLower(next), map[string]any{"stock_return_id": id, "reason": reason})
	})
}
