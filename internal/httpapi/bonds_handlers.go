package httpapi

// Guarantees and price adjustment (0050). The kernel decides every
// permission and the database computes every figure; these handlers only
// parse and answer.
//
//	GET  /v1/guarantees?contract=            register, standing, advance cover
//	POST /v1/guarantees                       register a guarantee
//	POST /v1/guarantees/{id}/change           reduce value / extend expiry {amount?, expires_on?, reason}
//	POST /v1/guarantees/{id}/close            {outcome RELEASED|CALLED, reference, reason, called_amount?}
//	GET  /v1/price-indices                    series and their values
//	POST /v1/price-indices                    {code, name, source}
//	POST /v1/price-index-values               {index_code, period, value, source_ref}; /{id}/submit|approve|reject
//	POST /v1/escalation/formulas              {contract_id, clause_ref, fixed_portion, base_period, terms}; /{id}/submit|approve|reject
//	GET  /v1/escalation?contract=             formula, certificates, adjustments
//	GET  /v1/price-adjustments/preview?ipc=&period=
//	POST /v1/price-adjustments                {ipc_id, period}; /{id}/submit|approve|reject
//	POST /v1/ld-terms                         {contract_id, clause_ref, completion_date, rate_per_day, cap_amount}; /{id}/submit|approve|reject (0051)
//	GET  /v1/contracts/{id}/time              time for completion, agreed extensions, damages, obligations
//	GET  /v1/obligations; POST /v1/obligations; POST /v1/obligations/{id}/close {status DONE|WAIVED, closed_on, note}

import (
	"context"
	"net/http"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerBonds() {
	m := s.mux
	k := s.cfg.Kernel
	reply := func(w http.ResponseWriter, out any, err error) {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
	m.Handle("GET /v1/guarantees", s.authed(func(w http.ResponseWriter, r *http.Request) {
		c := r.URL.Query().Get("contract")
		if c != "" && !validIDs(w, c) {
			return
		}
		out, err := k.Guarantees(r.Context(), principalFrom(r.Context()), c)
		reply(w, out, err)
	}))
	m.Handle("POST /v1/guarantees", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.GuaranteeInput
		if !decode(w, r, &b) || !validIDs(w, b.ContractID) {
			return
		}
		id, err := k.RegisterGuarantee(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/guarantees/{id}/change", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b kernel.GuaranteeChange
		if !decode(w, r, &b) {
			return
		}
		err := k.ChangeGuarantee(r.Context(), principalFrom(r.Context()), id, b)
		reply(w, map[string]any{"id": id, "ok": true}, err)
	}))
	m.Handle("POST /v1/guarantees/{id}/close", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b kernel.GuaranteeClose
		if !decode(w, r, &b) {
			return
		}
		err := k.CloseGuarantee(r.Context(), principalFrom(r.Context()), id, b)
		reply(w, map[string]any{"id": id, "ok": true}, err)
	}))

	m.Handle("GET /v1/price-indices", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.PriceIndices(r.Context(), principalFrom(r.Context()))
		reply(w, map[string]any{"indices": out}, err)
	}))
	m.Handle("POST /v1/price-indices", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Code   string `json:"code"`
			Name   string `json:"name"`
			Source string `json:"source"`
		}
		if !decode(w, r, &b) {
			return
		}
		if err := k.CreatePriceIndex(r.Context(), principalFrom(r.Context()), b.Code, b.Name, b.Source); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"code": b.Code})
	}))
	m.Handle("POST /v1/price-index-values", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.IndexValueInput
		if !decode(w, r, &b) {
			return
		}
		id, err := k.ProposeIndexValue(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/price-index-values/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitIndexValue(ctx, p, id)
	})))
	m.Handle("POST /v1/price-index-values/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideIndexValue(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/price-index-values/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideIndexValue(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/escalation/formulas/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitFormula(ctx, p, id)
	})))
	m.Handle("POST /v1/escalation/formulas/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideFormula(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/escalation/formulas/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideFormula(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/price-adjustments/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitAdjustment(ctx, p, id)
	})))
	m.Handle("POST /v1/price-adjustments/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideAdjustment(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/price-adjustments/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideAdjustment(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/escalation/formulas", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.FormulaInput
		if !decode(w, r, &b) || !validIDs(w, b.ContractID) {
			return
		}
		id, err := k.CreateFormula(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/escalation", s.authed(func(w http.ResponseWriter, r *http.Request) {
		c := r.URL.Query().Get("contract")
		if !validIDs(w, c) {
			return
		}
		out, err := k.Escalation(r.Context(), principalFrom(r.Context()), c)
		reply(w, out, err)
	}))
	m.Handle("GET /v1/price-adjustments/preview", s.authed(func(w http.ResponseWriter, r *http.Request) {
		ipc := r.URL.Query().Get("ipc")
		if !validIDs(w, ipc) {
			return
		}
		out, err := k.PreviewAdjustment(r.Context(), principalFrom(r.Context()), ipc, r.URL.Query().Get("period"))
		reply(w, out, err)
	}))
	m.Handle("POST /v1/price-adjustments", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			IPCID  string `json:"ipc_id"`
			Period string `json:"period"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.IPCID) {
			return
		}
		id, err := k.PrepareAdjustment(r.Context(), principalFrom(r.Context()), b.IPCID, b.Period)
		s.created(w, id, err)
	}))
	// Liquidated damages, extension of time, obligations (0051).
	m.Handle("POST /v1/ld-terms", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.LDTermsInput
		if !decode(w, r, &b) || !validIDs(w, b.ContractID) {
			return
		}
		id, err := k.CreateLDTerms(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/ld-terms/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitLDTerms(ctx, p, id)
	})))
	m.Handle("POST /v1/ld-terms/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideLDTerms(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/ld-terms/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideLDTerms(ctx, p, id, false, b.Reason)
	})))
	m.Handle("GET /v1/contracts/{id}/time", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.ContractTimePosition(r.Context(), principalFrom(r.Context()), id)
		reply(w, out, err)
	}))
	m.Handle("GET /v1/obligations", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.Obligations(r.Context(), principalFrom(r.Context()))
		reply(w, map[string]any{"obligations": out}, err)
	}))
	m.Handle("POST /v1/obligations", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.ObligationInput
		if !decode(w, r, &b) || !validIDs(w, b.ContractID) {
			return
		}
		id, err := k.AddObligation(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/obligations/{id}/close", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			Status   string `json:"status"`
			ClosedOn string `json:"closed_on"`
			Note     string `json:"note"`
		}
		if !decode(w, r, &b) {
			return
		}
		err := k.CloseObligation(r.Context(), principalFrom(r.Context()), id, b.Status, b.ClosedOn, b.Note)
		reply(w, map[string]any{"id": id, "ok": true}, err)
	}))
}

// Revenue over time and the WIP adjustment (0053).
//
//	GET  /v1/revenue/policies; POST /v1/revenue/policies; POST /v1/revenue/policies/{id}/approve|reject
//	GET  /v1/projects/{id}/revenue-runs; GET /v1/projects/{id}/revenue-preview?period_end=
//	POST /v1/projects/{id}/revenue-runs {period_end}; POST /v1/revenue-runs/{id}/cancel {reason}
func (s *Server) registerRevenue() {
	m := s.mux
	k := s.cfg.Kernel
	reply := func(w http.ResponseWriter, out any, err error) {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
	m.Handle("GET /v1/revenue/projects", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.RevenueProjects(r.Context(), principalFrom(r.Context()))
		reply(w, map[string]any{"projects": out}, err)
	}))
	m.Handle("GET /v1/revenue/policies", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.RevenuePolicies(r.Context(), principalFrom(r.Context()))
		reply(w, map[string]any{"policies": out}, err)
	}))
	m.Handle("POST /v1/revenue/policies", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.RevenuePolicyInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID, b.ContractAssetAcct, b.ContractLiabilityAc) {
			return
		}
		id, err := k.ProposeRevenuePolicy(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/revenue/policies/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideRevenuePolicy(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/revenue/policies/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideRevenuePolicy(ctx, p, id, false, b.Reason)
	})))
	m.Handle("GET /v1/projects/{id}/revenue-runs", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.RevenueRuns(r.Context(), principalFrom(r.Context()), id)
		reply(w, map[string]any{"runs": out}, err)
	}))
	m.Handle("GET /v1/projects/{id}/revenue-preview", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.PreviewRevenue(r.Context(), principalFrom(r.Context()), id, r.URL.Query().Get("period_end"))
		reply(w, out, err)
	}))
	m.Handle("POST /v1/projects/{id}/revenue-runs", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			PeriodEnd string `json:"period_end"`
		}
		if !decode(w, r, &b) {
			return
		}
		rid, err := k.PrepareRevenueRun(r.Context(), principalFrom(r.Context()), id, b.PeriodEnd)
		s.created(w, rid, err)
	}))
	m.Handle("POST /v1/revenue-runs/{id}/cancel", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CancelRevenueRun(ctx, p, id, b.Reason)
	})))
}
