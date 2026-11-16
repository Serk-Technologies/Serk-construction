package httpapi

// Resource rate library and rate build-ups (EST-06; 0094). Lists and details
// are the generic /v1/p2p/resource_rate and /v1/p2p/rate_buildup.
//
//	POST /v1/resource-rates {legal_entity_id, code, description, kind, unit, currency, rate, source, region, confidence, fx_date?, valid_from, valid_to}
//	POST /v1/resource-rates/{id}/approve {reason?}; POST /v1/resource-rates/{id}/reject {reason}
//	POST /v1/rate-buildups {project_id, code, description, unit, currency, price_date, overhead_pct, contingency_pct, escalation_pct, margin_pct, lines}
//	POST /v1/rate-buildups/{id}/lines {lines: [{resource_rate_id, quantity, wastage_pct}]}
//	POST /v1/rate-buildups/{id}/lines/{no} {quantity, wastage_pct}
//	POST /v1/rate-buildups/{id}/percentages {overhead_pct, contingency_pct, escalation_pct, margin_pct}
//	POST /v1/rate-buildups/{id}/submit
//	POST /v1/rate-buildups/{id}/approve {reason?}; POST /v1/rate-buildups/{id}/reject {reason}
//	POST /v1/rate-buildups/{id}/revise {price_date}
//	GET  /v1/rate-buildups/{id}/figures

import (
	"context"
	"net/http"
	"strconv"

	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerEstimates() {
	m, k := s.mux, s.cfg.Kernel
	ok := func(w http.ResponseWriter, id string, err error) {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
	}
	m.Handle("POST /v1/resource-rates", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.ResourceRateInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID) {
			return
		}
		id, err := k.RecordResourceRate(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	// .../approve and .../reject, as the console's document actions call them.
	m.Handle("POST /v1/resource-rates/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideResourceRate(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/resource-rates/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideResourceRate(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/rate-buildups/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideRateBuildup(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/rate-buildups/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideRateBuildup(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/rate-buildups", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.RateBuildupInput
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		for _, l := range b.Lines {
			if !validIDs(w, l.ResourceRateID) {
				return
			}
		}
		id, err := k.CreateRateBuildup(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/rate-buildups/{id}/lines", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Lines []kernel.RateBuildupLineInput `json:"lines"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		for _, l := range b.Lines {
			if !validIDs(w, l.ResourceRateID) {
				return
			}
		}
		ok(w, id, k.AddRateBuildupLines(r.Context(), principalFrom(r.Context()), id, b.Lines))
	}))
	m.Handle("POST /v1/rate-buildups/{id}/lines/{no}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		no, err := strconv.Atoi(r.PathValue("no"))
		if err != nil || no < 1 {
			writeErr(w, http.StatusNotFound, "not_found", "no such line")
			return
		}
		var b struct {
			Quantity   decimal.Decimal `json:"quantity"`
			WastagePct decimal.Decimal `json:"wastage_pct"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		ok(w, id, k.CorrectRateBuildupLine(r.Context(), principalFrom(r.Context()), id, no, b.Quantity, b.WastagePct))
	}))
	m.Handle("POST /v1/rate-buildups/{id}/percentages", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Overhead    decimal.Decimal `json:"overhead_pct"`
			Contingency decimal.Decimal `json:"contingency_pct"`
			Escalation  decimal.Decimal `json:"escalation_pct"`
			Margin      decimal.Decimal `json:"margin_pct"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		ok(w, id, k.SetRateBuildupPercentages(r.Context(), principalFrom(r.Context()), id, b.Overhead, b.Contingency, b.Escalation, b.Margin))
	}))
	m.Handle("POST /v1/rate-buildups/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitRateBuildup(ctx, p, id)
	})))
	m.Handle("POST /v1/rate-buildups/{id}/revise", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			PriceDate string `json:"price_date"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		nid, err := k.ReviseRateBuildup(r.Context(), principalFrom(r.Context()), id, b.PriceDate)
		s.created(w, nid, err)
	}))
	m.Handle("GET /v1/rate-buildups/{id}/figures", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validIDs(w, id) {
			return
		}
		out, err := k.BuildupFigures(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
}
