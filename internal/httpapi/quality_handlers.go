package httpapi

// Quality and site records (0065).
//
//	GET  /v1/quality; POST /v1/itps; POST /v1/itps/{id}/submit|approve|reject|cancel
//	POST /v1/inspection-requests; POST /v1/inspection-requests/{id}/pass|fail
//	POST /v1/ncrs; POST /v1/ncrs/{id}/action|close; POST /v1/material-tests
//	GET  /v1/site-records; POST /v1/punch-items; POST /v1/punch-items/{id}/fix|close|reopen
//	POST /v1/site-instructions; POST /v1/site-instructions/{id}/acknowledge|close
//	POST /v1/meetings; POST /v1/meetings/{id}/issue; POST /v1/meeting-actions/{id}/done

import (
	"context"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerQuality() {
	m := s.mux
	k := s.cfg.Kernel
	dec := func(fn func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error)) http.Handler {
		return s.authed(s.p2pDecision(fn))
	}
	reply := func(w http.ResponseWriter, out any, err error) {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
	m.Handle("GET /v1/quality", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.QualityView(r.Context(), principalFrom(r.Context()))
		reply(w, out, err)
	}))
	m.Handle("POST /v1/itps", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.ITPIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.CreateITP(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/itps/{id}/submit", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitITP(ctx, p, id)
	}))
	m.Handle("POST /v1/itps/{id}/approve", dec(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideITP(ctx, p, id, true, b.Reason)
	}))
	m.Handle("POST /v1/itps/{id}/reject", dec(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideITP(ctx, p, id, false, b.Reason)
	}))
	m.Handle("POST /v1/itps/{id}/cancel", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.CancelITP(ctx, p, id)
	}))
	m.Handle("POST /v1/inspection-requests", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.InspectionIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID, b.ITPID) {
			return
		}
		id, err := k.RequestInspection(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/inspection-requests/{id}/pass", dec(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideInspection(ctx, p, id, true, b.Note)
	}))
	m.Handle("POST /v1/inspection-requests/{id}/fail", dec(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideInspection(ctx, p, id, false, b.Note)
	}))
	m.Handle("POST /v1/ncrs", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.NCRIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.RaiseNCR(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/ncrs/{id}/action", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			RootCause        string          `json:"root_cause"`
			CorrectiveAction string          `json:"corrective_action"`
			CostImpact       decimal.Decimal `json:"cost_impact"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		reply(w, map[string]any{"id": id}, k.ActionNCR(r.Context(), principalFrom(r.Context()), id, b.RootCause, b.CorrectiveAction, b.CostImpact))
	}))
	m.Handle("POST /v1/ncrs/{id}/close", dec(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CloseNCR(ctx, p, id, b.Note)
	}))
	m.Handle("POST /v1/material-tests", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.MaterialTestIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, passed, err := k.RecordMaterialTest(r.Context(), principalFrom(r.Context()), b)
		reply(w, map[string]any{"id": id, "passed": passed}, err)
	}))
	// ---- site records
	m.Handle("GET /v1/site-records", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.SiteView(r.Context(), principalFrom(r.Context()))
		reply(w, out, err)
	}))
	m.Handle("POST /v1/punch-items", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.PunchIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.RaisePunch(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/punch-items/{id}/fix", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.FixPunch(ctx, p, id)
	}))
	m.Handle("POST /v1/punch-items/{id}/close", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.ClosePunch(ctx, p, id)
	}))
	m.Handle("POST /v1/punch-items/{id}/reopen", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.ReopenPunch(ctx, p, id)
	}))
	m.Handle("POST /v1/site-instructions", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.InstructionIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		if b.ChangeEventID != "" && !validIDs(w, b.ChangeEventID) {
			return
		}
		id, err := k.IssueInstruction(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/site-instructions/{id}/acknowledge", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.AckInstruction(ctx, p, id)
	}))
	m.Handle("POST /v1/site-instructions/{id}/close", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.CloseInstruction(ctx, p, id)
	}))
	m.Handle("POST /v1/meetings", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.MeetingIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.CreateMeeting(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/meetings/{id}/issue", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.IssueMinutes(ctx, p, id)
	}))
	m.Handle("POST /v1/meeting-actions/{id}/done", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.CompleteMeetingAction(ctx, p, id)
	}))
}
