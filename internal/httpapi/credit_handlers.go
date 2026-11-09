package httpapi

// Rules of credit and installed quantities (0067).
//
//	GET  /v1/credit; POST /v1/credit-rules; POST /v1/credit-rules/{id}/approve; POST /v1/credit-rules/{id}/steps/{seq}/complete {completed_on}
//	POST /v1/installed-quantities; POST /v1/progress-from-rules {project_id, status_date}

import (
	"context"
	"net/http"
	"strconv"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerCredit() {
	m := s.mux
	k := s.cfg.Kernel
	reply := func(w http.ResponseWriter, out any, err error) {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
	m.Handle("GET /v1/credit", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.CreditView(r.Context(), principalFrom(r.Context()))
		reply(w, out, err)
	}))
	m.Handle("POST /v1/credit-rules", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.CreditRuleIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.CreateCreditRule(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/credit-rules/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.ApproveCreditRule(ctx, p, id)
	})))
	m.Handle("POST /v1/credit-rules/{id}/steps/{seq}/complete", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		seq, err := strconv.Atoi(r.PathValue("seq"))
		var b struct {
			On string `json:"completed_on"`
		}
		if err != nil || !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		reply(w, map[string]any{"id": id}, k.CompleteCreditStep(r.Context(), principalFrom(r.Context()), id, seq, b.On))
	}))
	m.Handle("POST /v1/installed-quantities", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.InstalledIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		reply(w, map[string]any{"ok": true}, k.RecordInstalled(r.Context(), principalFrom(r.Context()), b))
	}))
	m.Handle("POST /v1/progress-from-rules", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Project string `json:"project_id"`
			On      string `json:"status_date"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Project) {
			return
		}
		id, err := k.ProgressFromRules(r.Context(), principalFrom(r.Context()), b.Project, b.On)
		s.created(w, id, err)
	}))
}
