package httpapi

// Consolidation and intercompany (0062).
//
//	GET  /v1/consolidation; GET /v1/consolidation-runs/{id}
//	POST /v1/consolidation-groups; POST /v1/consolidation-groups/{id}/approve|reject
//	POST /v1/intercompany-accounts {account_id, counterparty_entity_id, role}
//	POST /v1/consolidation-runs; POST /v1/consolidation-runs/{id}/approve|reject|cancel

import (
	"context"
	"net/http"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerConsolidation() {
	m := s.mux
	k := s.cfg.Kernel
	m.Handle("GET /v1/consolidation", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.ConsolidationView(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("GET /v1/consolidation-runs/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validIDs(w, id) {
			return
		}
		out, err := k.ConsolidationReport(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/consolidation-groups", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.GroupInput
		if !decode(w, r, &b) || !validIDs(w, b.ParentID) {
			return
		}
		for _, mb := range b.Members {
			if !validIDs(w, mb.EntityID) {
				return
			}
		}
		id, err := k.ProposeGroup(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/consolidation-groups/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideGroup(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/consolidation-groups/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideGroup(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/intercompany-accounts", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Account      string `json:"account_id"`
			Counterparty string `json:"counterparty_entity_id"`
			Role         string `json:"role"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Account, b.Counterparty) {
			return
		}
		id, err := k.DesignateIntercompany(r.Context(), principalFrom(r.Context()), b.Account, b.Counterparty, b.Role)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/consolidation-runs", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.ConsolidationRunInput
		if !decode(w, r, &b) || !validIDs(w, b.GroupID) {
			return
		}
		id, err := k.PrepareConsolidation(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/consolidation-runs/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideConsolidation(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/consolidation-runs/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideConsolidation(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/consolidation-runs/{id}/cancel", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CancelConsolidation(ctx, p, id, b.Reason)
	})))
}
