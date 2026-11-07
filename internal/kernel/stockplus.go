// Units of measure, stock reservations and reorder points (WP-1.6; SRS INV-04,
// INV-05, INV-07). Database side: migration 0041.
//
//   - Stock is kept in the item's base unit. A UoM conversion ("1 bag = 0.05 t")
//     is a controlled document (prepared by one person, approved by another);
//     only approved conversions are ever applied, with exact decimals.
//   - A reservation sets stock aside for a project. The database refuses to
//     reserve more than is on hand and unreserved, and refuses an issue that
//     would eat into someone else's reservation. An issue made against a
//     reservation consumes it.
//   - Reorder rules (minimum, maximum, reorder quantity per item and store)
//     drive the low-stock report. Nothing orders automatically.
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

// ---------------------------------------------------------------- conversions

// CreateUomConversion drafts a conversion. stockItemID may be empty for a
// conversion that applies to every item whose base unit is toUnit.
func (s *Service) CreateUomConversion(ctx context.Context, p auth.Principal, stockItemID, fromUnit, toUnit string, factor decimal.Decimal, note string) (string, error) {
	if !factor.IsPositive() {
		return "", fmt.Errorf("%w: the factor must be positive", ErrInvalidInput)
	}
	var id string
	err := s.p2pTx(ctx, p, auth.PermStockItemManage, func(ctx context.Context, tx pgx.Tx) error {
		if stockItemID != "" {
			if _, err := s.visibleDoc(ctx, tx, p, "stock_item", stockItemID, false); err != nil {
				return err
			}
		}
		var item any
		if stockItemID != "" {
			item = stockItemID
		}
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO uom_conversions (stock_item_id, from_uom, to_uom, factor, note, created_by)
			VALUES ($1::uuid, btrim($2), btrim($3), $4, NULLIF(btrim($5), ''), $6) RETURNING id::text`, item, fromUnit, toUnit, factor, note, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "uom.conversion.create", "uom_conversion", id, "uom.conversion.created",
			map[string]any{"uom_conversion_id": id, "from": fromUnit, "to": toUnit, "factor": factor.String()})
	})
	return id, err
}

// SubmitUomConversion submits a draft conversion (by its author).
func (s *Service) SubmitUomConversion(ctx context.Context, p auth.Principal, id string) error {
	return s.submitDoc(ctx, p, auth.PermStockItemManage, "uom_conversion", "uom_conversions", id, "uom.conversion.submit", "uom.conversion.submitted")
}

// DecideUomConversion approves or rejects a submitted conversion (a different person).
func (s *Service) DecideUomConversion(ctx context.Context, p auth.Principal, id string, approve bool, reason string) error {
	return s.decideGovernance(ctx, p, auth.PermStockItemApprove, "uom_conversion", "uom_conversions", id, "uom.conversion.decide", "unit conversion", approve, reason, nil)
}

// ToBaseUnit converts a quantity in unit into the item's base unit. A quantity
// already in the base unit (or with no unit) is returned as it is.
func (s *Service) ToBaseUnit(ctx context.Context, p auth.Principal, stockItemID string, qty decimal.Decimal, unit string) (decimal.Decimal, error) {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return qty, nil
	}
	out := qty
	err := s.p2pTx(ctx, p, auth.PermP2PRead, func(ctx context.Context, tx pgx.Tx) error {
		var base string
		if err := tx.QueryRow(ctx, `SELECT unit FROM stock_items WHERE id = $1::uuid`, stockItemID).Scan(&base); err != nil {
			return mapNoRows(err)
		}
		if strings.EqualFold(base, unit) {
			return nil
		}
		var factor *decimal.Decimal
		if err := tx.QueryRow(ctx, `SELECT uom_factor($1::uuid, $2)`, stockItemID, unit).Scan(&factor); err != nil {
			return err
		}
		if factor == nil {
			return fmt.Errorf("%w: there is no approved conversion from %s to %s for this item", ErrInvalidInput, unit, base)
		}
		out = qty.Mul(*factor)
		return nil
	})
	return out, err
}

// ---------------------------------------------------------------- reservations

// Reservation is stock set aside for a project.
type Reservation struct {
	ID          string          `json:"id"`
	ItemCode    string          `json:"item_code"`
	Warehouse   string          `json:"warehouse"`
	ProjectID   string          `json:"project_id"`
	Quantity    decimal.Decimal `json:"quantity"`
	Purpose     string          `json:"purpose"`
	Status      string          `json:"status"`
	ExpiresAt   time.Time       `json:"expires_at"`
	CreatedBy   string          `json:"created_by"`
	ReleaseNote string          `json:"release_reason,omitempty"`
}

// ReserveStock sets quantity (in unit, or the base unit when empty) aside for a project.
func (s *Service) ReserveStock(ctx context.Context, p auth.Principal, stockItemID, warehouseID, projectID string, qty decimal.Decimal, unit, purpose string, expires time.Time) (string, error) {
	if err := s.authz.RequireProject(p, auth.PermStockReserve, p.TenantID, projectID); err != nil {
		return "", err
	}
	if !qty.IsPositive() {
		return "", fmt.Errorf("%w: the quantity must be positive", ErrInvalidInput)
	}
	base, err := s.ToBaseUnit(ctx, p, stockItemID, qty, unit)
	if err != nil {
		return "", err
	}
	var id string
	err = s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "warehouse", warehouseID, false); err != nil {
			return err
		}
		var err error
		id, err = postgres.InsertReturningID(ctx, tx, `INSERT INTO stock_reservations (stock_item_id, warehouse_id, project_id, quantity, purpose, expires_at, created_by)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7) RETURNING id::text`, stockItemID, warehouseID, projectID, base, purpose, expires, p.UserID)
		if err != nil {
			return err
		}
		return s.record(ctx, tx, p, "stock.reserve", "stock_reservation", id, "stock.reserved",
			map[string]any{"stock_reservation_id": id, "quantity": base.String(), "project_id": projectID})
	})
	return id, err
}

// ReleaseReservation gives reserved stock back. The person who reserved it,
// or a storekeeper or inventory controller, may release it, with a reason.
func (s *Service) ReleaseReservation(ctx context.Context, p auth.Principal, id, reason string) error {
	if len(strings.TrimSpace(reason)) < 3 {
		return fmt.Errorf("%w: say why the reservation is released", ErrInvalidInput)
	}
	return s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var project, creator, status string
		if err := tx.QueryRow(ctx, `SELECT project_id::text, created_by, status FROM stock_reservations WHERE id = $1::uuid FOR UPDATE`, id).Scan(&project, &creator, &status); err != nil {
			return mapNoRows(err)
		}
		if !s.authz.ScopeOf(p).Allows(project) {
			return ErrNotFound
		}
		if creator != p.UserID && !s.authz.Has(p, auth.PermStockReceive) && !s.authz.Has(p, auth.PermStockAdjustApprove) {
			return ErrNotFound // indistinguishable from absence
		}
		if status != "ACTIVE" {
			return fmt.Errorf("%w: the reservation is %s", ErrInvalidInput, strings.ToLower(status))
		}
		if err := postgres.Exec1(ctx, tx, `UPDATE stock_reservations SET status = 'RELEASED', released_by = $2, release_reason = $3 WHERE id = $1::uuid`, id, p.UserID, reason); err != nil {
			return err
		}
		return s.record(ctx, tx, p, "stock.reservation.release", "stock_reservation", id, "stock.reservation.released", map[string]any{"stock_reservation_id": id, "reason": reason})
	})
}

// ListReservations lists reservations in the caller's project scope.
func (s *Service) ListReservations(ctx context.Context, p auth.Principal, status string) ([]Reservation, error) {
	if err := s.authz.Require(p, auth.PermP2PRead, p.TenantID); err != nil {
		return nil, err
	}
	scope := s.authz.ScopeOf(p)
	projects := scope.Projects
	if projects == nil {
		projects = []string{}
	}
	out := []Reservation{}
	err := s.store.WithTenant(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id::text, i.code, w.code, r.project_id::text, r.quantity, r.purpose,
			CASE WHEN r.status = 'ACTIVE' AND r.expires_at <= now() THEN 'EXPIRED' ELSE r.status END, r.expires_at, r.created_by, COALESCE(r.release_reason, '')
			FROM stock_reservations r JOIN stock_items i ON i.id = r.stock_item_id JOIN warehouses w ON w.id = r.warehouse_id
			WHERE ($1 = '' OR r.status = $1) AND ($2 OR r.project_id = ANY($3::uuid[])) ORDER BY r.created_at DESC LIMIT 500`, status, scope.TenantWide, projects)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Reservation
			if err := rows.Scan(&r.ID, &r.ItemCode, &r.Warehouse, &r.ProjectID, &r.Quantity, &r.Purpose, &r.Status, &r.ExpiresAt, &r.CreatedBy, &r.ReleaseNote); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// ---------------------------------------------------------------- reorder rules

// ReorderRule is a minimum, maximum and reorder quantity for an item in a store.
type ReorderRule struct {
	StockItemID string          `json:"stock_item_id"`
	WarehouseID string          `json:"warehouse_id"`
	Min         decimal.Decimal `json:"min_qty"`
	Max         decimal.Decimal `json:"max_qty"`
	ReorderQty  decimal.Decimal `json:"reorder_qty"`
}

// SetReorderRule creates or replaces the rule for an item in a store.
func (s *Service) SetReorderRule(ctx context.Context, p auth.Principal, r ReorderRule) error {
	if r.Min.IsNegative() || r.Max.LessThan(r.Min) || !r.ReorderQty.IsPositive() {
		return fmt.Errorf("%w: minimum cannot be negative, maximum cannot be below the minimum, and the reorder quantity must be positive", ErrInvalidInput)
	}
	return s.p2pTx(ctx, p, auth.PermStockItemManage, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.visibleDoc(ctx, tx, p, "stock_item", r.StockItemID, false); err != nil {
			return err
		}
		if _, err := s.visibleDoc(ctx, tx, p, "warehouse", r.WarehouseID, false); err != nil {
			return err
		}
		res, err := tx.Exec(ctx, `UPDATE stock_reorder_rules SET min_qty = $3, max_qty = $4, reorder_qty = $5, updated_by = $6, updated_at = now()
			WHERE stock_item_id = $1::uuid AND warehouse_id = $2::uuid`, r.StockItemID, r.WarehouseID, r.Min, r.Max, r.ReorderQty, p.UserID)
		if err != nil {
			return err
		}
		if res.RowsAffected() == 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO stock_reorder_rules (stock_item_id, warehouse_id, min_qty, max_qty, reorder_qty, created_by)
				VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6)`, r.StockItemID, r.WarehouseID, r.Min, r.Max, r.ReorderQty, p.UserID); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, p, "stock.reorder_rule.set", "stock_item", r.StockItemID, "stock.reorder_rule.set",
			map[string]any{"stock_item_id": r.StockItemID, "warehouse_id": r.WarehouseID, "min": r.Min.String(), "max": r.Max.String(), "reorder_qty": r.ReorderQty.String()})
	})
}
