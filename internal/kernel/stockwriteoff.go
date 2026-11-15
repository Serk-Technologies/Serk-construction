// Stock quarantine and write-offs (SUP-05.4, first part; migration 0088).
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

// CreateQuarantineWarehouse creates a quarantine store (0088): stock reaches
// and leaves it by approved transfers; nothing is issued or reserved from it.
// Whether a warehouse is a quarantine store is fixed when it is created.
func (s *Service) CreateQuarantineWarehouse(ctx context.Context, p auth.Principal, entity, projectID, code, name string) (string, error) {
	return s.createWarehouse(ctx, p, entity, projectID, code, name, true)
}

// StockWriteoffInput removes damaged, expired or lost stock at its cost.
type StockWriteoffInput struct {
	WarehouseID      string          `json:"warehouse_id"`
	StockItemID      string          `json:"stock_item_id"`
	ExpenseAccountID string          `json:"expense_account_id"`
	Quantity         decimal.Decimal `json:"quantity"`
	Unit             string          `json:"unit,omitempty"`
	WriteoffDate     time.Time       `json:"-"`
	Reason           string          `json:"reason"`
}

// CreateStockWriteoff prepares a DRAFT write-off (stock.count on the warehouse).
func (s *Service) CreateStockWriteoff(ctx context.Context, p auth.Principal, in StockWriteoffInput) (string, error) {
	if err := stockDate(in.WriteoffDate); err != nil {
		return "", err
	}
	if len(strings.TrimSpace(in.Reason)) < 5 {
		return "", fmt.Errorf("%w: say why the stock is written off", ErrInvalidInput)
	}
	qty, err := s.ToBaseUnit(ctx, p, in.StockItemID, in.Quantity, in.Unit)
	if err != nil {
		return "", err
	}
	var id string
	err = s.p2pTx(ctx, p, auth.PermStockCount, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "warehouse", in.WarehouseID, false); err != nil {
			return err
		}
		num, err := postgres.NextNumber(ctx, tx, "SWO")
		if err != nil {
			return err
		}
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO stock_writeoffs (legal_entity_id, number, warehouse_id, stock_item_id, expense_account_id, quantity, writeoff_date, reason, currency, created_by)
			SELECT w.legal_entity_id, $1, w.id, $3::uuid, $4::uuid, $5, $6, $7, le.functional_currency, $8
			  FROM warehouses w JOIN legal_entities le ON le.id = w.legal_entity_id WHERE w.id = $2::uuid RETURNING id::text`,
			num, in.WarehouseID, in.StockItemID, in.ExpenseAccountID, qty, in.WriteoffDate, strings.TrimSpace(in.Reason), p.UserID)
		if err != nil {
			return mapNoRows(err)
		}
		return s.record(ctx, tx, p, "stock.writeoff.create", "stock_writeoff", id, "stock.writeoff_prepared", map[string]any{"stock_writeoff_id": id, "number": num})
	})
	return id, err
}

// ApproveStockWriteoff approves a DRAFT write-off (stock.adjust.approve, not its
// preparer): the database removes the stock at cost and the STK_SCRAP journal
// is generated in the same transaction for finance to approve and post.
func (s *Service) ApproveStockWriteoff(ctx context.Context, p auth.Principal, id string) (string, error) {
	var jid string
	err := s.p2pTx(ctx, p, auth.PermStockAdjustApprove, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, auth.PermStockAdjustApprove, "stock_writeoff", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if err := auth.CheckMakerChecker(p, d.CreatedBy, "stock write-off"); err != nil {
			return err
		}
		if err := s.authorize(ctx, tx, p, "stock.writeoff.approve", "stock_writeoff", id, "APPROVED"); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE stock_writeoffs SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid`, id, p.UserID); err != nil {
			return err
		}
		jid, err = s.stockJournal(ctx, tx, p, "STK_SCRAP", "stock_writeoffs", "writeoff_date", id)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "stock.writeoff.approve", "stock_writeoff", id, "stock.written_off", map[string]any{"stock_writeoff_id": id, "journal_id": jid})
	})
	return jid, err
}

// RejectStockWriteoff (approver) or CancelStockWriteoff (preparer) closes a DRAFT write-off with a reason.
func (s *Service) RejectStockWriteoff(ctx context.Context, p auth.Principal, id, reason string) error {
	return s.closeStockWriteoff(ctx, p, auth.PermStockAdjustApprove, id, "REJECTED", "stock.writeoff.reject", reason)
}

// CancelStockWriteoff withdraws the preparer's own DRAFT write-off.
func (s *Service) CancelStockWriteoff(ctx context.Context, p auth.Principal, id, reason string) error {
	return s.closeStockWriteoff(ctx, p, auth.PermStockCount, id, "CANCELLED", "stock.writeoff.cancel", reason)
}

func (s *Service) closeStockWriteoff(ctx context.Context, p auth.Principal, perm auth.Permission, id, next, action, reason string) error {
	if err := requireReason(reason); err != nil {
		return err
	}
	return s.p2pTx(ctx, p, perm, func(ctx context.Context, tx pgx.Tx) error {
		d, err := s.loadDoc(ctx, tx, p, perm, "stock_writeoff", id)
		if err != nil {
			return err
		}
		if err := requireStatus(d, "DRAFT"); err != nil {
			return err
		}
		if next == "CANCELLED" && d.CreatedBy != p.UserID {
			return fmt.Errorf("%w: only the preparer cancels a write-off", auth.ErrForbidden)
		}
		if next == "REJECTED" {
			if err := auth.CheckMakerChecker(p, d.CreatedBy, "stock write-off"); err != nil {
				return err
			}
		}
		if err := s.authorize(ctx, tx, p, action, "stock_writeoff", id, next); err != nil {
			return err
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE stock_writeoffs SET status = $2, decision_reason = $3 WHERE id = $1::uuid`, id, next, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return s.record(ctx, tx, p, action, "stock_writeoff", id, "stock.writeoff_"+strings.ToLower(next), map[string]any{"stock_writeoff_id": id, "reason": reason})
	})
}
