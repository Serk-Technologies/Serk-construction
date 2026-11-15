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
//
// Part 2, fuel (0055; ADR 0022). A dispense is a stock issue; it is approved through POST /v1/stock-issues/{id}/approve.
//
//	GET  /v1/fleet/fuel-items                                      stock items designated as fuel
//	POST /v1/fleet/fuel-items                                      {stock_item_id}
//	POST /v1/fleet/fuel-standards                                  {equipment_id, litres_per_unit, effective_from, basis}
//	GET  /v1/fleet/fuel-dispenses?equipment=                       latest fillings
//	POST /v1/fleet/fuel-dispenses                                  {equipment_id, warehouse_id, stock_item_id, cost_code_id, expense_account_id, litres, meter_reading?, operator, dispensed_on, client_ref?}
//	GET  /v1/fleet/fuel-reconciliation?from=&to=&project=          dispensed, logged and expected fuel per machine, with differences to explain
//
// Part 3, maintenance (0056; ADR 0023). A part is a stock issue; it is approved through POST /v1/stock-issues/{id}/approve.
//
//	POST /v1/maintenance-work-orders                               {equipment_id, kind, description, plan_id?}; /{id}/submit|approve|reject|cancel
//	POST /v1/maintenance-work-orders/{id}/complete                 {work_done, labour_hours}
//	POST /v1/maintenance-work-orders/{id}/close                    the certifier accepts the work
//	POST /v1/maintenance-work-orders/{id}/send-back                {reason}: the certifier returns it to the workshop
//	GET  /v1/maintenance-work-orders/{id}/parts                    parts requested, with their stock issues
//	POST /v1/maintenance-work-orders/{id}/parts                    {warehouse_id, stock_item_id, cost_code_id, expense_account_id, quantity, project_id?}
//	POST /v1/maintenance-plans                                     {equipment_id, title, every_units?, every_days?}
//	GET  /v1/fleet/maintenance-due                                 preventive plans, most urgent first
//	GET  /v1/equipment/{id}/maintenance                            work orders and out-of-service hours of one machine

import (
	"context"
	"net/http"
	"strconv"

	"github.com/shopspring/decimal"

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
	m.Handle("GET /v1/fleet/fuel-items", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.ListFuelItems(r.Context(), principalFrom(r.Context()))
		reply(w, map[string]any{"items": out}, err)
	}))
	m.Handle("POST /v1/fleet/fuel-items", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			StockItemID string `json:"stock_item_id"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.StockItemID) {
			return
		}
		reply(w, map[string]any{"stock_item_id": b.StockItemID, "fuel": true}, k.DesignateFuelItem(r.Context(), principalFrom(r.Context()), b.StockItemID))
	}))
	m.Handle("POST /v1/fleet/fuel-standards", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.FuelStandardInput
		if !decode(w, r, &b) || !validIDs(w, b.EquipmentID) {
			return
		}
		id, err := k.SetFuelStandard(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/fleet/fuel-dispenses", s.authed(func(w http.ResponseWriter, r *http.Request) {
		eq := r.URL.Query().Get("equipment")
		if eq != "" && !validIDs(w, eq) {
			return
		}
		out, err := k.ListFuelDispenses(r.Context(), principalFrom(r.Context()), eq)
		reply(w, map[string]any{"items": out}, err)
	}))
	m.Handle("POST /v1/fleet/fuel-dispenses", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			kernel.FuelDispenseInput
			DispensedOn day `json:"dispensed_on"`
		}
		if !decode(w, r, &b) || !validIDs(w, b.EquipmentID, b.WarehouseID, b.StockItemID, b.CostCodeID, b.ExpenseAccountID) {
			return
		}
		b.FuelDispenseInput.DispensedOn = b.DispensedOn.Time
		out, err := k.DispenseFuel(r.Context(), principalFrom(r.Context()), b.FuelDispenseInput)
		if err != nil {
			s.fail(w, err)
			return
		}
		status := http.StatusCreated
		if out.AlreadyExists {
			status = http.StatusOK
		}
		writeJSON(w, status, out)
	}))
	m.Handle("GET /v1/fleet/fuel-reconciliation", s.authed(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if p := q.Get("project"); p != "" && !validIDs(w, p) {
			return
		}
		out, err := k.FuelReconciliationOf(r.Context(), principalFrom(r.Context()), q.Get("from"), q.Get("to"), q.Get("project"))
		reply(w, out, err)
	}))
	m.Handle("POST /v1/maintenance-work-orders", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.WorkOrderInput
		if !decode(w, r, &b) || !validIDs(w, b.EquipmentID) {
			return
		}
		if b.PlanID != "" && !validIDs(w, b.PlanID) {
			return
		}
		id, err := k.CreateWorkOrder(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("POST /v1/maintenance-work-orders/{id}/submit", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.SubmitWorkOrder(ctx, p, id)
	})))
	m.Handle("POST /v1/maintenance-work-orders/{id}/approve", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideWorkOrder(ctx, p, id, true, b.Reason)
	})))
	m.Handle("POST /v1/maintenance-work-orders/{id}/reject", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.DecideWorkOrder(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/maintenance-work-orders/{id}/cancel", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CancelWorkOrder(ctx, p, id, b.Reason)
	})))
	m.Handle("POST /v1/maintenance-work-orders/{id}/close", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, _ decisionBody) (any, error) {
		return nil, k.CloseWorkOrder(ctx, p, id, true, "")
	})))
	m.Handle("POST /v1/maintenance-work-orders/{id}/send-back", s.authed(s.p2pDecision(func(ctx context.Context, p auth.Principal, id string, b decisionBody) (any, error) {
		return nil, k.CloseWorkOrder(ctx, p, id, false, b.Reason)
	})))
	m.Handle("POST /v1/maintenance-work-orders/{id}/complete", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b struct {
			WorkDone    string          `json:"work_done"`
			LabourHours decimal.Decimal `json:"labour_hours"`
		}
		if !decode(w, r, &b) {
			return
		}
		reply(w, map[string]any{"id": id, "status": "COMPLETED"}, k.CompleteWorkOrder(r.Context(), principalFrom(r.Context()), id, b.WorkDone, b.LabourHours))
	}))
	m.Handle("GET /v1/maintenance-work-orders/{id}/parts", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.WorkOrderParts(r.Context(), principalFrom(r.Context()), id)
		reply(w, map[string]any{"items": out}, err)
	}))
	m.Handle("POST /v1/maintenance-work-orders/{id}/parts", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var b kernel.WorkOrderPartInput
		if !decode(w, r, &b) || !validIDs(w, b.WarehouseID, b.StockItemID, b.CostCodeID, b.ExpenseAccountID) {
			return
		}
		if b.ProjectID != "" && !validIDs(w, b.ProjectID) {
			return
		}
		issue, err := k.RequestWorkOrderPart(r.Context(), principalFrom(r.Context()), id, b)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"stock_issue_id": issue})
	}))
	m.Handle("POST /v1/maintenance-plans", s.authed(func(w http.ResponseWriter, r *http.Request) {
		var b kernel.MaintenancePlanInput
		if !decode(w, r, &b) || !validIDs(w, b.EquipmentID) {
			return
		}
		id, err := k.CreateMaintenancePlan(r.Context(), principalFrom(r.Context()), b)
		s.created(w, id, err)
	}))
	m.Handle("GET /v1/fleet/maintenance-due", s.authed(func(w http.ResponseWriter, r *http.Request) {
		out, err := k.MaintenanceDue(r.Context(), principalFrom(r.Context()))
		reply(w, map[string]any{"items": out}, err)
	}))
	m.Handle("GET /v1/equipment/{id}/maintenance", s.authed(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		out, err := k.EquipmentMaintenanceOf(r.Context(), principalFrom(r.Context()), id)
		reply(w, out, err)
	}))
}
