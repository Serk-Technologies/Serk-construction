package httpapi

// Timesheets, equipment and HSE (0064).
//
//	GET  /v1/roster?project=; POST /v1/timesheets; GET /v1/timesheets/{id}; POST /v1/timesheets/{id}/submit|approve|reject|cancel
//	POST /v1/pay-runs/{id}/import-timesheets
//	GET  /v1/equipment; POST /v1/equipment; POST /v1/equipment/{id}/status
//	POST /v1/equipment-usage; POST /v1/equipment-usage/{id}/submit|approve|reject|cancel
//	POST /v1/equipment-work-orders; POST /v1/equipment-work-orders/{id}/complete; POST /v1/equipment-hire-runs
//	GET  /v1/hse; POST /v1/incidents; POST /v1/incidents/{id}/investigate|actions|close; POST /v1/capas/{id}/done|verify
//	POST /v1/permits; POST /v1/permits/{id}/issue|accept|close|cancel
//	POST /v1/inductions; POST /v1/toolbox-talks; POST /v1/safety-inspections; POST /v1/hse/induction-gate

import (
	"context"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerField() {
	m := s.mux
	k := s.cfg.Kernel
	dec := func(fn func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error)) http.Handler {
		return s.authed(s.p2pDecision(fn))
	}
	json := func(w http.ResponseWriter, out any, err error) {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
	// ---- timesheets
	m.Handle("GET /v1/roster", s.authed(func(w http.ResponseWriter, r *http.Request) {
		pr := r.URL.Query().Get("project")
		if !validIDs(w, pr) {
			return
		}
		out, err := k.Roster(r.Context(), principalFrom(r.Context()), pr)
		json(w, map[string]any{"employees": out}, err)
	}))
	m.Handle("POST /v1/timesheets", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.TimesheetIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		for _, l := range b.Lines {
			if !validIDs(w, l.EmployeeID) {
				return
			}
		}
		id, err := k.CreateTimesheet(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/timesheets/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validIDs(w, id) {
			return
		}
		out, err := k.TimesheetDetail(r.Context(), principalFrom(r.Context()), id)
		json(w, out, err)
	}))
	m.Handle("POST /v1/timesheets/{id}/submit", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitTimesheet(ctx, p, id)
	}))
	m.Handle("POST /v1/timesheets/{id}/approve", dec(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideTimesheet(ctx, p, id, true, b.Reason)
	}))
	m.Handle("POST /v1/timesheets/{id}/reject", dec(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideTimesheet(ctx, p, id, false, b.Reason)
	}))
	m.Handle("POST /v1/timesheets/{id}/cancel", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.CancelTimesheet(ctx, p, id)
	}))
	m.Handle("POST /v1/pay-runs/{id}/import-timesheets", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		n, err := k.ImportTimesheetsToPayRun(ctx, p, id)
		return map[string]any{"id": id, "imported": n}, err
	}))
	// ---- equipment
	m.Handle("GET /v1/equipment", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.EquipmentView(r.Context(), principalFrom(r.Context()))
		json(w, out, err)
	}))
	m.Handle("POST /v1/equipment", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.EquipmentIn
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID) {
			return
		}
		id, err := k.RegisterEquipment(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/equipment/{id}/status", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Status   string `json:"status"`
			Location string `json:"location"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		json(w, map[string]any{"id": id}, k.SetEquipmentStatus(r.Context(), principalFrom(r.Context()), id, b.Status, b.Location))
	}))
	m.Handle("POST /v1/equipment-usage", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.UsageIn
		if !decode(w, r, &b) || !validIDs(w, b.EquipmentID, b.ProjectID) {
			return
		}
		id, err := k.RecordUsage(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/equipment-usage/{id}/submit", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitUsage(ctx, p, id)
	}))
	m.Handle("POST /v1/equipment-usage/{id}/approve", dec(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideUsage(ctx, p, id, true, b.Reason)
	}))
	m.Handle("POST /v1/equipment-usage/{id}/reject", dec(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideUsage(ctx, p, id, false, b.Reason)
	}))
	m.Handle("POST /v1/equipment-usage/{id}/cancel", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.CancelUsage(ctx, p, id)
	}))
	m.Handle("POST /v1/equipment-work-orders", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.WorkOrderIn
		if !decode(w, r, &b) || !validIDs(w, b.EquipmentID) {
			return
		}
		id, err := k.OpenWorkOrder(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/equipment-work-orders/{id}/complete", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Cost decimal.Decimal `json:"cost"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		json(w, map[string]any{"id": id}, k.CompleteWorkOrder(r.Context(), principalFrom(r.Context()), id, b.Cost))
	}))
	m.Handle("POST /v1/equipment-hire-runs", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Entity   string `json:"legal_entity_id"`
			Start    string `json:"period_start"`
			End      string `json:"period_end"`
			Cost     string `json:"cost_account_id"`
			Recovery string `json:"recovery_account_id"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Entity, b.Cost, b.Recovery) {
			return
		}
		id, jids, err := k.PrepareHireRun(r.Context(), principalFrom(r.Context()), b.Entity, b.Start, b.End, b.Cost, b.Recovery)
		json(w, map[string]any{"id": id, "journal_ids": jids}, err)
	}))
	// ---- HSE
	m.Handle("GET /v1/hse", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.HSEView(r.Context(), principalFrom(r.Context()))
		json(w, out, err)
	}))
	m.Handle("POST /v1/incidents", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.IncidentIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.ReportIncident(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/incidents/{id}/investigate", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			RootCause string `json:"root_cause"`
			Findings  string `json:"findings"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		json(w, map[string]any{"id": id}, k.InvestigateIncident(r.Context(), principalFrom(r.Context()), id, b.RootCause, b.Findings))
	}))
	m.Handle("POST /v1/incidents/{id}/actions", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Description string `json:"description"`
			Owner       string `json:"owner_name"`
			DueOn       string `json:"due_on"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		cid, err := k.RaiseCAPA(r.Context(), principalFrom(r.Context()), id, b.Description, b.Owner, b.DueOn)
		s.created(w, cid, err)
	}))
	m.Handle("POST /v1/incidents/{id}/close", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var b struct {
			Note string `json:"note"`
		}
		if !validIDs(w, id) || !decode(w, r, &b) {
			return
		}
		json(w, map[string]any{"id": id}, k.CloseIncident(r.Context(), principalFrom(r.Context()), id, b.Note))
	}))
	m.Handle("POST /v1/capas/{id}/done", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.CompleteCAPA(ctx, p, id)
	}))
	m.Handle("POST /v1/capas/{id}/verify", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.VerifyCAPA(ctx, p, id)
	}))
	m.Handle("POST /v1/permits", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.PermitIn
		if !decode(w, r, &b) || !validIDs(w, b.ProjectID) {
			return
		}
		id, err := k.RequestPermit(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/permits/{id}/issue", dec(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.IssuePermit(ctx, p, id, b.Note)
	}))
	m.Handle("POST /v1/permits/{id}/accept", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.AcceptPermit(ctx, p, id)
	}))
	m.Handle("POST /v1/permits/{id}/close", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.ClosePermit(ctx, p, id)
	}))
	m.Handle("POST /v1/permits/{id}/cancel", dec(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.CancelPermit(ctx, p, id)
	}))
	m.Handle("POST /v1/inductions", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Employee   string `json:"employee_id"`
			Project    string `json:"project_id"`
			InductedOn string `json:"inducted_on"`
			ValidTo    string `json:"valid_to"`
			Topics     string `json:"topics"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Employee, b.Project) {
			return
		}
		json(w, map[string]any{"ok": true}, k.RecordInduction(r.Context(), principalFrom(r.Context()), b.Employee, b.Project, b.InductedOn, b.ValidTo, b.Topics))
	}))
	m.Handle("POST /v1/toolbox-talks", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Project   string `json:"project_id"`
			HeldOn    string `json:"held_on"`
			Topic     string `json:"topic"`
			Attendees int    `json:"attendees"`
			Notes     string `json:"notes"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Project) {
			return
		}
		json(w, map[string]any{"ok": true}, k.RecordToolboxTalk(r.Context(), principalFrom(r.Context()), b.Project, b.HeldOn, b.Topic, b.Attendees, b.Notes))
	}))
	m.Handle("POST /v1/safety-inspections", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Project  string `json:"project_id"`
			On       string `json:"inspected_on"`
			Area     string `json:"area"`
			Score    int    `json:"score"`
			Findings string `json:"findings"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.Project) {
			return
		}
		json(w, map[string]any{"ok": true}, k.RecordSafetyInspection(r.Context(), principalFrom(r.Context()), b.Project, b.On, b.Area, b.Score, b.Findings))
	}))
	m.Handle("POST /v1/hse/induction-gate", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			On bool `json:"on"`
		}
		if !decode(w, r, &b) {
			return
		}
		json(w, map[string]any{"on": b.On}, k.SetInductionGate(r.Context(), principalFrom(r.Context()), b.On))
	}))
}
