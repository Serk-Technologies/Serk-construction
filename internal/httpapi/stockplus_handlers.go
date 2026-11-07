package httpapi

// Units of measure, stock reservations and reorder rules (WP-1.6). Rules live
// in internal/kernel/stockplus.go and migration 0041; handlers only decode,
// call the kernel and map errors.
//
//	POST /v1/stock/uom-conversions                  prepare a conversion (DRAFT)
//	POST /v1/stock/uom-conversions/{id}/submit|approve|reject
//	POST /v1/stock/reservations                     reserve stock for a project
//	GET  /v1/stock/reservations?status=             reservations in your scope
//	POST /v1/stock/reservations/{id}/release        give reserved stock back (reason)
//	PUT  /v1/stock/reorder-rules                    set minimum / maximum / reorder quantity
//	(low stock: report /v1/reports/catalogue/low-stock)

import (
	"context"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerStockPlus() {
	m := s.mux
	k := s.cfg.Kernel
	m.Handle("POST /v1/stock/uom-conversions", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			StockItemID string          `json:"stock_item_id"`
			FromUnit    string          `json:"from_unit"`
			ToUnit      string          `json:"to_unit"`
			Factor      decimal.Decimal `json:"factor"`
			Note        string          `json:"note"`
		}
		if !decode(w, r, &b) || (b.StockItemID != "" && !validIDs(w, b.StockItemID)) {
			return
		}
		id, err := k.CreateUomConversion(r.Context(), principalFrom(r.Context()), b.StockItemID, b.FromUnit, b.ToUnit, b.Factor, b.Note)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/stock/uom-conversions/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitUomConversion(ctx, p, id)
	})))
	m.Handle("POST /v1/stock/uom-conversions/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideUomConversion(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/stock/uom-conversions/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideUomConversion(ctx, p, id, false, b.Reason)
	})))

	m.Handle("POST /v1/stock/reservations", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			StockItemID string          `json:"stock_item_id"`
			WarehouseID string          `json:"warehouse_id"`
			ProjectID   string          `json:"project_id"`
			Quantity    decimal.Decimal `json:"quantity"`
			Unit        string          `json:"unit"`
			Purpose     string          `json:"purpose"`
			ExpiresAt   time.Time       `json:"expires_at"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.StockItemID, b.WarehouseID, b.ProjectID) {
			return
		}
		id, err := k.ReserveStock(r.Context(), principalFrom(r.Context()), b.StockItemID, b.WarehouseID, b.ProjectID, b.Quantity, b.Unit, b.Purpose, b.ExpiresAt)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/stock/reservations", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.ListReservations(r.Context(), principalFrom(r.Context()), r.URL.Query().Get("status"))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"reservations": out})
	}))
	m.Handle("POST /v1/stock/reservations/{id}/release", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.ReleaseReservation(ctx, p, id, b.Reason)
	})))
	m.Handle("PUT /v1/stock/reorder-rules", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.ReorderRule
		if !decode(w, r, &b) || !validIDs(w, b.StockItemID, b.WarehouseID) {
			return
		}
		if err := k.SetReorderRule(r.Context(), principalFrom(r.Context()), b); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
}
