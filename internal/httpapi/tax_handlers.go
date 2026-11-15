package httpapi

// Tax returns and e-invoicing (0063).
//
//	GET  /v1/tax[?return=]; POST /v1/tax-accounts {account_id, kind}
//	POST /v1/tax-returns {legal_entity_id, kind, period_start, period_end}; POST /v1/tax-returns/{id}/approve|reject|file|cancel
//	POST /v1/customer-invoices/{id}/e-invoice

import (
	"context"
	"net/http"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerTax() {
	m := s.mux
	k := s.cfg.Kernel
	m.Handle("GET /v1/tax", s.authed(func(w http.ResponseWriter, r *http.Request) {
		ret := r.URL.Query().Get("return")
		if ret != "" && !validIDs(w, ret) {
			return
		}
		out, err := k.TaxView(r.Context(), principalFrom(r.Context()), ret)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/tax-accounts", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Account string `json:"account_id"`
			Kind    string `json:"kind"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Account) {
			return
		}
		id, err := k.DesignateTaxAccount(r.Context(), principalFrom(r.Context()), b.Account, b.Kind)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/tax-returns", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Entity string `json:"legal_entity_id"`
			Kind   string `json:"kind"`
			Start  string `json:"period_start"`
			End    string `json:"period_end"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Entity) {
			return
		}
		id, err := k.PrepareTaxReturn(r.Context(), principalFrom(r.Context()), b.Entity, b.Kind, b.Start, b.End)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/tax-returns/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideTaxReturn(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/tax-returns/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideTaxReturn(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/tax-returns/{id}/file", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.FileTaxReturn(ctx, p, id, b.Note)
	})))
	m.Handle("POST /v1/tax-returns/{id}/cancel", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CancelTaxReturn(ctx, p, id, b.Reason)
	})))
	m.Handle("POST /v1/customer-invoices/{id}/e-invoice", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validIDs(w, id) {
			return
		}
		out, err := k.SubmitEInvoice(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
}
