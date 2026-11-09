package httpapi

// Subcontracts (0055). The kernel decides every permission; the database
// computes every certificate figure.
//
//	POST /v1/subcontract-policies; POST /v1/subcontract-policies/{id}/approve|reject
//	POST /v1/subcontracts; GET /v1/subcontracts/{id}; POST /v1/subcontracts/{id}/submit|approve|reject
//	POST /v1/subcontracts/{id}/applications {period_to, lines}
//	POST /v1/subcontract-applications/{id}/submit; POST /v1/subcontract-applications/{id}/certify {lines}; .../reject {reason}
//	POST /v1/subcontracts/{id}/backcharges {reason, evidence_ref, amount}; POST /v1/subcontract-backcharges/{id}/submit|approve|reject

import (
	"context"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerSubcontracts() {
	m := s.mux
	k := s.cfg.Kernel
	m.Handle("POST /v1/subcontract-policies", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.SubcontractPolicyInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID, b.Payable, b.Retention, b.Advance, b.WHT, b.Backcharge) {
			return
		}
		id, err := k.ProposeSubcontractPolicy(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/subcontract-policies/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideSubcontractPolicy(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/subcontract-policies/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideSubcontractPolicy(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/subcontracts", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.SubcontractInput
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID, b.SupplierID) {
			return
		}
		id, err := k.CreateSubcontract(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/subcontracts/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.GetSubcontract(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/subcontracts/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitSubcontract(ctx, p, id)
	})))
	m.Handle("POST /v1/subcontracts/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideSubcontract(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/subcontracts/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideSubcontract(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/subcontracts/{id}/applications", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			PeriodTo string                   `json:"period_to"`
			Lines    []kernel.ApplicationLine `json:"lines"`
		}
		if !decode(w, r, &b) {
			return
		}
		aid, err := k.CreateApplication(r.Context(), principalFrom(r.Context()), id, b.PeriodTo, b.Lines)
		s.created(w, aid, err)
	}))
	m.Handle("POST /v1/subcontract-applications/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitApplication(ctx, p, id)
	})))
	m.Handle("POST /v1/subcontract-applications/{id}/certify", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			Lines []kernel.ApplicationLine `json:"lines"`
		}
		if !decode(w, r, &b) {
			return
		}
		jid, err := k.CertifyApplication(r.Context(), principalFrom(r.Context()), id, b.Lines)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "journal_id": jid})
	}))
	m.Handle("POST /v1/subcontract-applications/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.RejectApplication(ctx, p, id, b.Reason)
	})))
	m.Handle("POST /v1/subcontracts/{id}/backcharges", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			Reason   string          `json:"reason"`
			Evidence string          `json:"evidence_ref"`
			Amount   decimal.Decimal `json:"amount"`
		}
		if !decode(w, r, &b) {
			return
		}
		bid, err := k.CreateBackcharge(r.Context(), principalFrom(r.Context()), id, b.Reason, b.Evidence, b.Amount)
		s.created(w, bid, err)
	}))
	m.Handle("POST /v1/subcontract-backcharges/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitBackcharge(ctx, p, id)
	})))
	m.Handle("POST /v1/subcontract-backcharges/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideBackcharge(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/subcontract-backcharges/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideBackcharge(ctx, p, id, false, b.Reason)
	})))
	// Paying certificates and subcontractor documents (0056).
	m.Handle("POST /v1/subcontractor-documents", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.SubDocumentInput
		if !decode(w, r, &b) || !validIDs(w, b.SupplierID) {
			return
		}
		id, err := k.RegisterSubDocument(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/suppliers/{id}/subcontractor-documents", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.SubDocuments(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"documents": out})
	}))
	m.Handle("POST /v1/subcontract-payments", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.SubPaymentInput
		if !decode(w, r, &b) || !validIDs(w, b.SubcontractID, b.SupplierBankID, b.CompanyBankID) {
			return
		}
		id, err := k.PrepareSubPayment(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/subcontract-payments/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitSubPayment(ctx, p, id)
	})))
	m.Handle("POST /v1/subcontract-payments/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideSubPayment(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/subcontract-payments/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideSubPayment(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/subcontract-payments/{id}/release", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		jid, err := k.ReleaseSubPayment(ctx, p, id)
		return map[string]any{"journal_id": jid}, err
	})))
	m.Handle("POST /v1/subcontract-payments/{id}/cancel", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CancelSubPayment(ctx, p, id, b.Reason)
	})))
}
