package httpapi

// Project risk register (PLB-04.1; 0080).
//
//	POST /v1/project-risks {project_id, title, category, cause, effect, owner, response, probability, cost_impact, schedule_days}
//	POST /v1/project-risks/{id}/assess {probability, cost_impact, schedule_days, note}; POST /v1/project-risks/{id}/close {reason}
//	GET  /v1/projects/{id}/risks

import (
	"net/http"

	"github.com/serk-group/serk-construction/internal/kernel"
)

func (s *Server) registerRisks() {
	m, k := s.mux, s.cfg.Kernel
	m.Handle("POST /v1/project-risks", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.RiskInput
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.RaiseRisk(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/project-risks/{id}/assess", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b kernel.RiskAssessment
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		if err := k.AssessRisk(r.Context(), principalFrom(r.Context()), id, b); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
	}))
	m.Handle("POST /v1/project-risks/{id}/close", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b decisionBody
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		if err := k.CloseRisk(r.Context(), principalFrom(r.Context()), id, b.Reason); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "CLOSED"})
	}))
	m.Handle("GET /v1/projects/{id}/risks", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.ProjectRiskRegister(r.Context(), principalFrom(r.Context()), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))
}
