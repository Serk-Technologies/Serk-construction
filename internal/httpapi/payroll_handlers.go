package httpapi

// Workforce and payroll (0061).
//
//	GET  /v1/payroll[?run=]; POST /v1/payroll-policies; POST /v1/payroll-policies/{id}/approve|reject
//	POST /v1/employees; POST /v1/employees/{id}/leave {ends_on}
//	POST /v1/payroll-rule-sets; POST /v1/payroll-rule-sets/{id}/submit|approve|reject
//	POST /v1/pay-runs {legal_entity_id, period_start, period_end, pay_date}; POST /v1/pay-runs/{id}/inputs {inputs}
//	POST /v1/pay-runs/{id}/calculate|submit|approve|reject|cancel; POST /v1/pay-runs/{id}/release {company_bank_id}
//	GET  /v1/pay-runs/{id}/bank-file (CSV, treasury)

import (
	"context"
	"net/http"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerPayroll() {
	m := s.mux
	k := s.cfg.Kernel
	m.Handle("GET /v1/payroll", s.authed(func(w http.ResponseWriter, r *http.Request) {
		run := r.URL.Query().Get("run")
		if run != "" && !validIDs(w, run) {
			return
		}
		out, err := k.PayrollView(r.Context(), principalFrom(r.Context()), run)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/payroll-policies", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.PayrollPolicyInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID, b.Salary, b.PensionExpense, b.NetPayable, b.TaxPayable, b.PensionPayable) {
			return
		}
		id, err := k.ProposePayrollPolicy(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/payroll-policies/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecidePayrollPolicy(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/payroll-policies/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecidePayrollPolicy(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/employees", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.EmployeeInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID) {
			return
		}
		id, err := k.RegisterEmployee(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/employees/{id}/leave", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			EndsOn string `json:"ends_on"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		if err := k.EmployeeLeaves(r.Context(), principalFrom(r.Context()), id, b.EndsOn); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
	}))
	m.Handle("POST /v1/payroll-rule-sets", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.RuleSetInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID) {
			return
		}
		id, err := k.CreateRuleSet(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/payroll-rule-sets/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitRuleSet(ctx, p, id)
	})))
	m.Handle("POST /v1/payroll-rule-sets/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideRuleSet(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/payroll-rule-sets/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideRuleSet(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/pay-runs", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Entity string `json:"legal_entity_id"`
			Start  string `json:"period_start"`
			End    string `json:"period_end"`
			Pay    string `json:"pay_date"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Entity) {
			return
		}
		id, err := k.CreatePayRun(r.Context(), principalFrom(r.Context()), b.Entity, b.Start, b.End, b.Pay)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/pay-runs/{id}/inputs", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Inputs []kernel.PayInput `json:"inputs"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		for _, in := range b.Inputs {
			if !validIDs(w, in.EmployeeID) {
				return
			}
		}
		if err := k.AddPayInputs(r.Context(), principalFrom(r.Context()), id, b.Inputs); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "count": len(b.Inputs)})
	}))
	m.Handle("POST /v1/pay-runs/{id}/calculate", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.CalculatePayRun(ctx, p, id)
	})))
	m.Handle("POST /v1/pay-runs/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitPayRun(ctx, p, id)
	})))
	m.Handle("POST /v1/pay-runs/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecidePayRun(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/pay-runs/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecidePayRun(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/pay-runs/{id}/cancel", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CancelPayRun(ctx, p, id, b.Reason)
	})))
	m.Handle("POST /v1/pay-runs/{id}/release", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Bank string `json:"company_bank_id"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) || !validIDs(w, b.Bank) {
			return
		}
		jid, err := k.ReleasePayRun(r.Context(), principalFrom(r.Context()), id, b.Bank)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "journal_id": jid})
	}))
	m.Handle("GET /v1/pay-runs/{id}/bank-file", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validIDs(w, id) {
			return
		}
		p := principalFrom(r.Context())
		out, err := k.PayrollBankFile(r.Context(), p, id)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.cfg.Logger.Info("audit", "event", "payroll_bank_file", "tenant", p.TenantID, "sub", p.UserID, "run", id, "bytes", len(out))
		h := w.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Disposition", `attachment; filename="payroll-`+id[:8]+`.csv"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(out)
	}))
}
