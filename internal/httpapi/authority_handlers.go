package httpapi

// Authority matrix, delegation of authority and duty-conflict exceptions
// (WP-1.2). Business rules live in internal/kernel/authority.go and migration
// 0040; these handlers only decode, call the kernel and map errors.
//
//	POST /v1/authority/matrices                  prepare a matrix (DRAFT)
//	POST /v1/authority/matrices/{id}/submit|approve|reject
//	GET  /v1/authority/matrix                    the matrix in force
//	POST /v1/delegations                         lend part of your authority (DRAFT)
//	POST /v1/delegations/{id}/submit|approve|reject|revoke
//	GET  /v1/delegations                         delegations you lent or received
//	POST /v1/sod/exceptions                      prepare an exception (DRAFT)
//	POST /v1/sod/exceptions/{id}/submit|approve|reject
//	GET  /v1/sod/report                          duty conflicts in the role catalogue

import (
	"context"
	"net/http"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerAuthority(m *http.ServeMux) {
	k := s.cfg.Kernel
	dec := func(fn func(context.Context, auth.Principal, string, bool, string) error, approve bool) http.Handler {
		return s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
			return nil, fn(ctx, p, id, approve, b.Reason)
		}))
	}
	sub := func(fn func(context.Context, auth.Principal, string) error) http.Handler {
		return s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
			return nil, fn(ctx, p, id)
		}))
	}
	m.Handle("POST /v1/authority/matrices", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Reason string                 `json:"reason"`
			Rules  []kernel.AuthorityRule `json:"rules"`
		}
		if !decode(w, r, &b) {
			return
		}
		id, err := k.CreateAuthorityMatrix(r.Context(), principalFrom(r.Context()), b.Reason, b.Rules)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/authority/matrices/{id}/submit", sub(k.SubmitAuthorityMatrix))
	m.Handle("POST /v1/authority/matrices/{id}/approve", dec(k.DecideAuthorityMatrix, true))
	m.Handle("POST /v1/authority/matrices/{id}/reject", dec(k.DecideAuthorityMatrix, false))
	m.Handle("GET /v1/authority/matrix", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.CurrentAuthorityMatrix(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"matrix": out})
	}))

	m.Handle("POST /v1/delegations", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.DelegationRequest
		if !decode(w, r, &b) {
			return
		}
		id, err := k.CreateDelegation(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/delegations/{id}/submit", sub(k.SubmitDelegation))
	m.Handle("POST /v1/delegations/{id}/approve", dec(k.DecideDelegation, true))
	m.Handle("POST /v1/delegations/{id}/reject", dec(k.DecideDelegation, false))
	m.Handle("POST /v1/delegations/{id}/revoke", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.RevokeDelegation(ctx, p, id, b.Reason)
	})))
	m.Handle("GET /v1/delegations", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.MyDelegations(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"delegations": out})
	}))

	m.Handle("POST /v1/sod/exceptions", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.SoDExceptionRequest
		if !decode(w, r, &b) {
			return
		}
		id, err := k.CreateSoDException(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/sod/exceptions/{id}/submit", sub(k.SubmitSoDException))
	m.Handle("POST /v1/sod/exceptions/{id}/approve", dec(k.DecideSoDException, true))
	m.Handle("POST /v1/sod/exceptions/{id}/reject", dec(k.DecideSoDException, false))
	m.Handle("GET /v1/sod/report", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.SoDReport(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"conflicts": out})
	}))
}
