package httpapi

// SERK FLEET, part 1 (0054; ADR 0020). The kernel decides every permission
// and the database every rule; these handlers only parse and answer.
//
//	GET  /v1/equipment?status=&class=&project=&q=&cursor=&limit=   the register, a page at a time
//	POST /v1/equipment                                             register equipment (REGISTERED)
//	GET  /v1/equipment/{id}                                        one machine: moves open to the caller, history, reservations
//	POST /v1/equipment/{id}/move                                   {to, reason, project_id?}; to may be IN_SERVICE
//	GET  /v1/equipment/{id}/calendar?from=&to=                     reservations overlapping a period
//	POST /v1/equipment-reservations                                {equipment_id, project_id, purpose, starts_on, ends_on}; /{id}/submit|approve|reject|cancel
//	POST /v1/equipment-operators                                   {operator, equipment_class, certificate_ref, valid_from, valid_until}; /{id}/submit|approve|reject
//	POST /v1/equipment-logs                                        a daily log; /{id}/submit|approve|reject
//	POST /v1/equipment-meter-resets                                {equipment_id, kind, new_reading, reason}; /{id}/submit|approve|reject
//	GET  /v1/fleet/summary?from=&to=&project=                      counts by state and usage from approved logs

import (
	"context"
	"net/http"
	"strconv"

	"github.com/serk-group/serk-construction/internal/kernel"
	"github.com/serk-group/serk-construction/pkg/auth"
)

func (s *Server) registerFleet() {
	m := s.mux
	k := s.cfg.Kernel
	reply := func(w http.ResponseWriter, out any, err error) {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
	m.Handle("GET /v1/equipment", s.authed(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if p := q.Get("project"); p != "" && !validIDs(w, p) {
			return
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		out, err := k.ListEquipment(r.Context(), principalFrom(r.Context()), kernel.EquipmentFilter{Status: q.Get("status"), Class: q.Get("class"),
			ProjectID: q.Get("project"), Search: q.Get("q"), Cursor: q.Get("cursor"), Limit: limit})
		reply(w, out, err)
	}))
	m.Handle("POST /v1/equipment", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.EquipmentInput
		if !decode(w, r, &b) || !validIDs(w, b.LegalEntityID) {
			return
		}
		if b.SupplierID != "" && !validIDs(w, b.SupplierID) {
			return
		}
		id, err := k.RegisterEquipment(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/equipment/{id}", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.GetEquipment(r.Context(), principalFrom(r.Context()), id)
		reply(w, out, err)
	}))
	m.Handle("POST /v1/equipment/{id}/move", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			To        string `json:"to"`
			Reason    string `json:"reason"`
			ProjectID string `json:"project_id"`
		}
		if !decode(w, r, &b) {
			return
		}
		if b.ProjectID != "" && !validIDs(w, b.ProjectID) {
			return
		}
		status, err := k.MoveEquipment(r.Context(), principalFrom(r.Context()), id, b.To, b.Reason, b.ProjectID)
		reply(w, map[string]any{"id": id, "status": status}, err)
	}))
	m.Handle("GET /v1/equipment/{id}/calendar", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.EquipmentCalendar(r.Context(), principalFrom(r.Context()), id, r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		reply(w, map[string]any{"reservations": out}, err)
	}))
	m.Handle("GET /v1/fleet/summary", s.authed(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if p := q.Get("project"); p != "" && !validIDs(w, p) {
			return
		}
		out, err := k.FleetSummaryOf(r.Context(), principalFrom(r.Context()), q.Get("from"), q.Get("to"), q.Get("project"))
		reply(w, out, err)
	}))

	m.Handle("POST /v1/equipment-reservations", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.EquipmentReservationInput
		if !decode(w, r, &b) || !validIDs(w, b.EquipmentID, b.ProjectID) {
			return
		}
		id, err := k.CreateEquipmentReservation(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/equipment-operators", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.OperatorAuthInput
		if !decode(w, r, &b) {
			return
		}
		id, err := k.CreateOperatorAuth(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/equipment-logs", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.EquipmentLogInput
		if !decode(w, r, &b) || !validIDs(w, b.EquipmentID, b.ProjectID) {
			return
		}
		if b.CostCodeID != "" && !validIDs(w, b.CostCodeID) {
			return
		}
		id, err := k.CreateEquipmentLog(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/equipment-meter-resets", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.MeterResetInput
		if !decode(w, r, &b) || !validIDs(w, b.EquipmentID) {
			return
		}
		id, err := k.CreateMeterReset(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/equipment-reservations/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitEquipmentReservation(ctx, p, id)
	})))
	m.Handle("POST /v1/equipment-reservations/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideEquipmentReservation(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/equipment-reservations/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideEquipmentReservation(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/equipment-operators/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitOperatorAuth(ctx, p, id)
	})))
	m.Handle("POST /v1/equipment-operators/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideOperatorAuth(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/equipment-operators/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideOperatorAuth(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/equipment-logs/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitEquipmentLog(ctx, p, id)
	})))
	m.Handle("POST /v1/equipment-logs/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideEquipmentLog(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/equipment-logs/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideEquipmentLog(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/equipment-meter-resets/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitMeterReset(ctx, p, id)
	})))
	m.Handle("POST /v1/equipment-meter-resets/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideMeterReset(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/equipment-meter-resets/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideMeterReset(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/equipment-reservations/{id}/cancel", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CancelEquipmentReservation(ctx, p, id, b.Reason)
	})))
}
