package httpapi

// Project master, stage gates, regions, business units and the company
// calendar (WP-1.7). Rules live in internal/kernel/org.go and migration 0042.
//
//	GET  /v1/projects                           projects in your scope with their master data
//	POST /v1/projects                           create a PLANNED project in phase BID (project.create)
//	PUT  /v1/projects/{id}                      edit master data (project.update)
//	POST /v1/projects/{id}/gates                prepare a stage gate to {to_phase} with {notes}
//	POST /v1/project-gates/{id}/submit|approve|reject
//	POST /v1/projects/{id}/forecasts            open a cost forecast {period_end, note}
//	GET  /v1/forecasts/{id}                     the forecast with lines and totals
//	PUT  /v1/forecasts/{id}/lines/{code}        set a line {method, manual_etc, note}
//	POST /v1/forecasts/{id}/submit|approve|reject|cancel
//	POST /v1/change-events {project_id, title, source, description, revenue_impact, schedule_impact_days, lines[]}
//	GET  /v1/change-events/{id}                 the event with lines, totals and the budget draft it raised
//	POST /v1/change-events/{id}/submit|approve|reject
//	POST /v1/notices, GET /v1/notices?contract=, POST /v1/notices/{id}/send {sent_on}
//	POST /v1/claims, POST /v1/claims/{id}/submit|approve|reject, POST /v1/claims/{id}/outcome
//	GET|POST /v1/org/regions, /v1/org/business-units
//	GET|POST /v1/org/calendar, DELETE /v1/org/calendar/{id}

import (
	"context"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerOrg() {
	m := s.mux
	k := s.cfg.Kernel
	m.Handle("GET /v1/projects", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.ListProjects(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": out, "phases": kernel.ProjectPhases})
	}))
	m.Handle("POST /v1/projects", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.ProjectInput
		if !decode(w, r, &b) || !validOptionalIDs(w, b.EntityID, b.CustomerID, b.RegionID, b.BusinessUnitID) {
			return
		}
		id, err := k.CreateProject(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("PUT /v1/projects/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b kernel.ProjectInput
		if !decode(w, r, &b) || !validOptionalIDs(w, b.CustomerID, b.RegionID, b.BusinessUnitID) {
			return
		}
		if err := k.UpdateProject(r.Context(), principalFrom(r.Context()), id, b); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
	}))
	m.Handle("POST /v1/projects/{id}/gates", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			ToPhase string `json:"to_phase"`
			Notes   string `json:"notes"`
		}
		if !decode(w, r, &b) {
			return
		}
		gid, err := k.CreateStageGate(r.Context(), principalFrom(r.Context()), id, b.ToPhase, b.Notes)
		s.created(w, gid, err)
	}))
	// Cost forecasting (0044): a cycle per project and month end, adjusted line by line.
	m.Handle("GET /v1/projects/{id}/s-curve", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.ProjectSCurve(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
	m.Handle("POST /v1/projects/{id}/forecasts", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			PeriodEnd string `json:"period_end"`
			Note      string `json:"note"`
		}
		if !decode(w, r, &b) {
			return
		}
		fid, err := k.CreateForecast(r.Context(), principalFrom(r.Context()), id, b.PeriodEnd, b.Note)
		s.created(w, fid, err)
	}))
	m.Handle("GET /v1/forecasts/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		c, err := k.GetForecast(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	}))
	m.Handle("PUT /v1/forecasts/{id}/lines/{code}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		code := r.PathValue("code")
		if !validIDs(w, code) {
			return
		}
		var b struct {
			Method    string           `json:"method"`
			ManualETC *decimal.Decimal `json:"manual_etc"`
			Note      string           `json:"note"`
		}
		if !decode(w, r, &b) {
			return
		}
		if err := k.SetForecastLine(r.Context(), principalFrom(r.Context()), id, code, b.Method, b.ManualETC, b.Note); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
	}))
	m.Handle("POST /v1/forecasts/{id}/cancel", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CancelForecast(ctx, p, id, b.Reason)
	})))
	m.Handle("POST /v1/forecasts/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitForecast(ctx, p, id)
	})))
	m.Handle("POST /v1/forecasts/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideForecast(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/forecasts/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideForecast(ctx, p, id, false, b.Reason)
	})))
	// Change events (0045).
	m.Handle("POST /v1/change-events", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.ChangeEventInput
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.CreateChangeEvent(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/change-events/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		e, err := k.GetChangeEvent(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, e)
	}))
	m.Handle("POST /v1/change-events/{id}/variations", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			VariationID string `json:"variation_id"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.VariationID) {
			return
		}
		if err := k.LinkVariation(r.Context(), principalFrom(r.Context()), id, b.VariationID); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
	}))
	m.Handle("POST /v1/change-events/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitChangeEvent(ctx, p, id)
	})))
	m.Handle("POST /v1/change-events/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideChangeEvent(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/change-events/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideChangeEvent(ctx, p, id, false, b.Reason)
	})))
	// Contract notices and claims (0046).
	m.Handle("POST /v1/notices", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.NoticeInput
		if !decode(w, r, &b) || !validIDs(w, b.ContractID) {
			return
		}
		id, err := k.CreateNotice(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/notices", s.authed(func(w http.ResponseWriter, r *http.Request) {
		c := r.URL.Query().Get("contract")
		if c != "" && !validIDs(w, c) {
			return
		}
		out, err := k.ListNotices(r.Context(), principalFrom(r.Context()), c)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"notices": out})
	}))
	m.Handle("POST /v1/notices/{id}/send", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			SentOn string `json:"sent_on"`
		}
		if !decode(w, r, &b) {
			return
		}
		if err := k.SendNotice(r.Context(), principalFrom(r.Context()), id, b.SentOn); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
	}))
	m.Handle("POST /v1/claims", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.ClaimInput
		if !decode(w, r, &b) || !validIDs(w, b.ContractID) {
			return
		}
		if b.NoticeID != "" && !validIDs(w, b.NoticeID) {
			return
		}
		id, err := k.CreateClaim(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/claims/{id}/outcome", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b kernel.ClaimOutcome
		if !decode(w, r, &b) {
			return
		}
		if err := k.RecordClaimOutcome(r.Context(), principalFrom(r.Context()), id, b); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "ok": true})
	}))
	m.Handle("POST /v1/claims/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitClaim(ctx, p, id)
	})))
	m.Handle("POST /v1/claims/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideClaim(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/claims/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideClaim(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/project-gates/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitStageGate(ctx, p, id)
	})))
	m.Handle("POST /v1/project-gates/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideStageGate(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/project-gates/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideStageGate(ctx, p, id, false, b.Reason)
	})))

	for path, kind := range map[string]string{"/v1/org/regions": "region", "/v1/org/business-units": "business_unit"} {
		kind := kind
		m.Handle("GET "+path, s.authed(func(w http.ResponseWriter, r *http.Request) {
			out, err := k.ListOrgItems(r.Context(), principalFrom(r.Context()), kind)
			if err != nil {
				s.fail(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": out})
		}))
		m.Handle("POST "+path, s.authed(func(w http.ResponseWriter, r *http.Request) {
			var b struct{ Code, Name string }
			if !decode(w, r, &b) {
				return
			}
			id, err := k.CreateOrgItem(r.Context(), principalFrom(r.Context()), kind, b.Code, b.Name)
			s.created(w, id, err)
		}))
	}
	m.Handle("GET /v1/org/calendar", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.ListCalendarExceptions(r.Context(), principalFrom(r.Context()))
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"days": out})
	}))
	m.Handle("POST /v1/org/calendar", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Day, Name, Kind string }
		if !decode(w, r, &b) {
			return
		}
		id, err := k.AddCalendarException(r.Context(), principalFrom(r.Context()), b.Day, b.Name, b.Kind)
		s.created(w, id, err)
	}))
	m.Handle("DELETE /v1/org/calendar/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := k.RemoveCalendarException(r.Context(), principalFrom(r.Context()), id); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
}

// validOptionalIDs accepts empty strings and canonical UUIDs.
func validOptionalIDs(w http.ResponseWriter, ids ...string) bool {
	for _, id := range ids {
		if id != "" && !canonicalUUID(id) {
			writeErr(w, http.StatusUnprocessableEntity, "validation", "an id is not valid")
			return false
		}
	}
	return true
}
