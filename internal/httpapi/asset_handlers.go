package httpapi

// Fixed assets (0057).
//
//	GET  /v1/assets; POST /v1/asset-classes; POST /v1/assets; POST /v1/assets/{id}/submit|approve|reject
//	GET  /v1/depreciation/preview?entity=&month=; POST /v1/depreciation-runs {legal_entity_id, month}; POST /v1/depreciation-runs/{id}/cancel
//	POST /v1/asset-disposals; POST /v1/asset-disposals/{id}/submit|approve|reject

import (
	"context"
	"net/http"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerAssets() {
	m := s.mux
	k := s.cfg.Kernel
	m.Handle("GET /v1/assets", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.AssetRegister(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/asset-classes", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.AssetClassInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID, b.CostAccount, b.AccumAccount, b.ExpenseAcct, b.GainLossAcct) {
			return
		}
		id, err := k.CreateAssetClass(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/assets", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.AssetInput
		if !decode(w, r, &b) || !validIDs(w, b.ClassID, b.CapitalisedFrom) {
			return
		}
		id, err := k.RegisterAsset(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/assets/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitAsset(ctx, p, id)
	})))
	m.Handle("POST /v1/assets/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		jid, err := k.DecideAsset(ctx, p, id, true, b.Reason)
		return map[string]any{"id": id, "journal_id": jid}, err
	})))
	m.Handle("POST /v1/assets/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		_, err := k.DecideAsset(ctx, p, id, false, b.Reason)
		return nil, err
	})))
	m.Handle("GET /v1/depreciation/preview", s.authed(func(w http.ResponseWriter, r *http.Request) {
		e := r.URL.Query().Get("entity")
		if !validIDs(w, e) {
			return
		}
		out, err := k.PreviewDepreciation(r.Context(), principalFrom(r.Context()), e, r.URL.Query().Get("month"))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"lines": out})
	}))
	m.Handle("POST /v1/depreciation-runs", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Entity string `json:"legal_entity_id"`
			Month  string `json:"month"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Entity) {
			return
		}
		id, err := k.PrepareDepreciation(r.Context(), principalFrom(r.Context()), b.Entity, b.Month)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/depreciation-runs/{id}/cancel", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CancelDepreciation(ctx, p, id, b.Reason)
	})))
	m.Handle("POST /v1/asset-disposals", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.DisposalInput
		if !decode(w, r, &b) || !validIDs(w, b.AssetID, b.ProceedsAccount) {
			return
		}
		id, err := k.ProposeDisposal(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/asset-disposals/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitDisposal(ctx, p, id)
	})))
	m.Handle("POST /v1/asset-disposals/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		jid, err := k.DecideDisposal(ctx, p, id, true, b.Reason)
		return map[string]any{"id": id, "journal_id": jid}, err
	})))
	m.Handle("POST /v1/asset-disposals/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		_, err := k.DecideDisposal(ctx, p, id, false, b.Reason)
		return nil, err
	})))
}

// Period-end (0058).
//
//	GET  /v1/fiscal-periods?entity=; POST /v1/fiscal-years {legal_entity_id, calendar, year}
//	GET  /v1/period-end; POST /v1/recurring-journals; POST /v1/recurring-journals/run {as_of}
//	POST /v1/accruals; POST /v1/accruals/reverse-due {as_of}
//	GET  /v1/overhead/preview?entity=&from=&to=&pool=&target=; POST /v1/overhead-allocations
func (s *Server) registerPeriodEnd() {
	m := s.mux
	k := s.cfg.Kernel
	reply := func(w http.ResponseWriter, code int, out any, err error) {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, code, out)
	}
	m.Handle("GET /v1/fiscal-periods", s.authed(func(w http.ResponseWriter, r *http.Request) {
		e := r.URL.Query().Get("entity")
		if !validIDs(w, e) {
			return
		}
		out, err := k.FiscalPeriods(r.Context(), principalFrom(r.Context()), e)
		reply(w, http.StatusOK, map[string]any{"periods": out}, err)
	}))
	m.Handle("POST /v1/fiscal-years", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Entity   string `json:"legal_entity_id"`
			Calendar string `json:"calendar"`
			Year     int    `json:"year"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Entity) {
			return
		}
		out, err := k.GenerateFiscalYear(r.Context(), principalFrom(r.Context()), b.Entity, b.Calendar, b.Year)
		reply(w, http.StatusCreated, map[string]any{"periods": out}, err)
	}))
	m.Handle("GET /v1/period-end", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.PeriodEndView(r.Context(), principalFrom(r.Context()))
		reply(w, http.StatusOK, out, err)
	}))
	m.Handle("POST /v1/recurring-journals", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.RecurringInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID) {
			return
		}
		id, err := k.CreateRecurring(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/recurring-journals/run", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			AsOf string `json:"as_of"`
		}
		if !decode(w, r, &b) {
			return
		}
		out, err := k.RunRecurring(r.Context(), principalFrom(r.Context()), b.AsOf)
		reply(w, http.StatusOK, map[string]any{"journals": out}, err)
	}))
	m.Handle("POST /v1/accruals", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.AccrualInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID) {
			return
		}
		id, err := k.CreateAccrual(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/accruals/reverse-due", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			AsOf string `json:"as_of"`
		}
		if !decode(w, r, &b) {
			return
		}
		out, err := k.RunAccrualReversals(r.Context(), principalFrom(r.Context()), b.AsOf)
		reply(w, http.StatusOK, map[string]any{"journals": out}, err)
	}))
	m.Handle("GET /v1/overhead/preview", s.authed(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		in := kernel.OverheadInput{LegalEntityID: q.Get("entity"), From: q.Get("from"), To: q.Get("to"), PoolAccount: q.Get("pool"), TargetAccount: q.Get("target")}
		if !validIDs(w, in.LegalEntityID, in.PoolAccount, in.TargetAccount) {
			return
		}
		out, err := k.PreviewOverhead(r.Context(), principalFrom(r.Context()), in)
		reply(w, http.StatusOK, map[string]any{"shares": out}, err)
	}))
	m.Handle("POST /v1/overhead-allocations", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.OverheadInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID, b.PoolAccount, b.TargetAccount) {
			return
		}
		id, err := k.AllocateOverhead(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
}

// Cash forecasting (0059).
//
//	GET /v1/cash-forecast?entity=; POST /v1/cash-plan; POST /v1/cash-plan/{id}/cancel {reason}
func (s *Server) registerCash() {
	m := s.mux
	k := s.cfg.Kernel
	m.Handle("GET /v1/cash-forecast", s.authed(func(w http.ResponseWriter, r *http.Request) {
		e := r.URL.Query().Get("entity")
		if !validIDs(w, e) {
			return
		}
		out, err := k.CashForecastOf(r.Context(), principalFrom(r.Context()), e)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/cash-plan", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.CashPlanInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID) {
			return
		}
		id, err := k.AddCashPlanItem(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/cash-plan/{id}/cancel", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CancelCashPlanItem(ctx, p, id, b.Reason)
	})))
}
