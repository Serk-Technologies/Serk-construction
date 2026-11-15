// Fleet maintenance (FLT-03; migration 0056, ADR 0023).
package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/fleet"
)

// TestMaintenance_WorkOrderAndReturnToService: a work order passes through
// four people's hands at most (preparer, authoriser, reporter, certifier), and
// a machine that has been under maintenance cannot return to service around it.
func TestMaintenance_WorkOrderAndReturnToService(t *testing.T) {
	w := newFuelWorld(t)
	f, c, fw := w.f, w.cast, w.fw
	eq := w.working(t, "M-100")
	officer2 := fw.officer
	officer2.UserID = "u-plant-officer-2"
	inspector2 := fw.inspector
	inspector2.UserID = "u-plant-inspector-2"

	// Who may prepare what.
	if _, err := f.svc.CreateWorkOrder(f.ctx, fw.officer, WorkOrderInput{EquipmentID: eq, Kind: "REPAINT", Description: "Repaint the counterweight"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an unknown kind: %v", err)
	}
	if _, err := f.svc.CreateWorkOrder(f.ctx, fw.officer, WorkOrderInput{EquipmentID: eq, Kind: "CORRECTIVE", Description: "short"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("no description: %v", err)
	}
	if _, err := f.svc.CreateWorkOrder(f.ctx, c.siteEng, WorkOrderInput{EquipmentID: eq, Kind: "PREVENTIVE", Description: "250 hour service as I see fit"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("site staff prepared a preventive work order: %v", err)
	}
	elsewhere := auth.Principal{UserID: "u-eng-9", TenantID: f.tenant, Roles: []string{"site_engineer"}, ProjectIDs: []string{"00000000-0000-4000-8000-000000000009"}, IssuedAt: time.Now().Add(-time.Minute), Kind: auth.KindHuman}
	if _, err := f.svc.CreateWorkOrder(f.ctx, elsewhere, WorkOrderInput{EquipmentID: eq, Kind: "BREAKDOWN", Description: "Engine will not start after refuelling"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("site staff of another project reported a breakdown of this machine: %v", err)
	}
	if _, err := f.svc.CreateWorkOrder(f.ctx, c.store, WorkOrderInput{EquipmentID: eq, Kind: "BREAKDOWN", Description: "Engine will not start after refuelling"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a storekeeper prepared a work order: %v", err)
	}

	// The site reports the breakdown; the machine stops.
	wo, err := f.svc.CreateWorkOrder(f.ctx, c.siteEng, WorkOrderInput{EquipmentID: eq, Kind: "BREAKDOWN", Description: "Engine will not start after refuelling; starter turns"})
	must(t, "breakdown work order", err)
	_, err = f.svc.MoveEquipment(f.ctx, c.siteEng, eq, "BREAKDOWN", "Will not start", "")
	must(t, "breakdown", err)
	_, err = f.svc.MoveEquipment(f.ctx, c.siteEng, eq, "UNDER_MAINTENANCE", "Mechanic called", "")
	must(t, "to maintenance", err)

	// Authorisation: not before submission, not by the preparer's role, not without a fresh second factor, not by an AI.
	if err := f.svc.DecideWorkOrder(f.ctx, fw.manager, wo, true, ""); err == nil {
		t.Fatal("a draft work order was authorised")
	}
	if err := f.svc.SubmitWorkOrder(f.ctx, fw.officer, wo); err == nil {
		t.Fatal("someone other than the preparer submitted the work order")
	}
	must(t, "submit", f.svc.SubmitWorkOrder(f.ctx, c.siteEng, wo))
	if err := f.svc.DecideWorkOrder(f.ctx, fw.officer, wo, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("authorised without fleet.approve: %v", err)
	}
	stale := fw.manager
	stale.AuthTime = time.Now().Add(-time.Hour)
	if err := f.svc.DecideWorkOrder(f.ctx, stale, wo, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("authorised without fresh MFA: %v", err)
	}
	ai := fw.manager
	ai.Kind = auth.KindAI
	if err := f.svc.DecideWorkOrder(f.ctx, ai, wo, true, ""); err == nil {
		t.Fatal("an AI principal authorised a repair")
	}
	// Nothing is reported or certified before authorisation.
	if err := f.svc.CompleteWorkOrder(f.ctx, fw.officer, wo, "Fuel filter and water separator replaced", dec("2")); err == nil {
		t.Fatal("work was reported complete before it was authorised")
	}
	must(t, "authorise", f.svc.DecideWorkOrder(f.ctx, fw.manager, wo, true, ""))

	// An open work order holds the machine, whichever way it tries to leave.
	if _, err := f.svc.MoveEquipment(f.ctx, fw.inspector, eq, "IN_SERVICE", "Started fine", ""); err == nil {
		t.Fatal("a machine returned to service with an open work order")
	}
	_, err = f.svc.MoveEquipment(f.ctx, c.siteEng, eq, "UNDER_INSPECTION", "Waiting for the inspector", "")
	must(t, "to inspection", err)
	if _, err := f.svc.MoveEquipment(f.ctx, fw.inspector, eq, "IN_SERVICE", "Started fine", ""); err == nil {
		t.Fatal("a machine left maintenance through inspection with an open work order")
	}

	// Parts: an ordinary stock issue, linked to the work order, charged to the machine's project.
	part := WorkOrderPartInput{WarehouseID: w.store, StockItemID: w.diesel, CostCodeID: w.costCode, ExpenseAccountID: w.acct["5000"], Quantity: dec("20"), Note: "Flushing fuel"}
	if _, err := f.svc.RequestWorkOrderPart(f.ctx, fw.manager, wo, part); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a part requested without stock.issue: %v", err)
	}
	bad := part
	bad.Quantity = dec("0")
	if _, err := f.svc.RequestWorkOrderPart(f.ctx, c.store, wo, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a part of zero quantity: %v", err)
	}
	issue, err := f.svc.RequestWorkOrderPart(f.ctx, c.store, wo, part)
	must(t, "part", err)
	if q, _ := w.balanceOf(t, w.diesel); !q.Equal(dec("3000")) {
		t.Fatalf("stock moved before the part's issue was approved: %s", q)
	}
	// The job is not reported complete while a part is still awaiting approval.
	if err := f.svc.CompleteWorkOrder(f.ctx, fw.officer, wo, "Fuel filter and water separator replaced", dec("2")); err == nil {
		t.Fatal("a work order was completed with a part still awaiting approval")
	}
	journal, err := f.svc.ApproveStockIssue(f.ctx, c.siteEng, issue)
	must(t, "approve part issue", err)
	w.financePost(t, journal)
	parts, err := f.svc.WorkOrderParts(f.ctx, c.pm, wo)
	must(t, "parts", err)
	if len(parts) != 1 || parts[0].IssueStatus != "APPROVED" || parts[0].Amount == nil || !parts[0].Amount.Equal(dec("1900")) || parts[0].ItemCode != "DIESEL" {
		t.Fatalf("parts: %+v", parts)
	}
	if q, _ := w.balanceOf(t, w.diesel); !q.Equal(dec("2980")) {
		t.Fatalf("after the part was approved the store holds %s, want 2980", q)
	}

	// Reported complete by plant staff; certified by someone else, with a fresh second factor.
	if err := f.svc.CompleteWorkOrder(f.ctx, c.siteEng, wo, "Fuel filter and water separator replaced", dec("2")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("completed without fleet.manage: %v", err)
	}
	if err := f.svc.CompleteWorkOrder(f.ctx, fw.officer, wo, "done", dec("2")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a completion report that says nothing: %v", err)
	}
	must(t, "complete", f.svc.CompleteWorkOrder(f.ctx, fw.officer, wo, "Fuel filter and water separator replaced; tank drained of water", dec("2.5")))
	if _, err := f.svc.RequestWorkOrderPart(f.ctx, c.store, wo, part); err == nil {
		t.Fatal("a part was requested for a work order already reported complete")
	}
	if err := f.svc.CloseWorkOrder(f.ctx, fw.officer, wo, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("closed without fleet.release: %v", err)
	}
	both := fw.inspector
	both.UserID, both.Roles = fw.officer.UserID, []string{"plant_inspector", "plant_officer"}
	if err := f.svc.CloseWorkOrder(f.ctx, both, wo, true, ""); err == nil {
		t.Fatal("the person who reported the work complete certified it")
	}
	staleInspector := fw.inspector
	staleInspector.AuthTime = time.Now().Add(-time.Hour)
	if err := f.svc.CloseWorkOrder(f.ctx, staleInspector, wo, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("certified without fresh MFA: %v", err)
	}
	aiInspector := fw.inspector
	aiInspector.Kind = auth.KindAI
	if err := f.svc.CloseWorkOrder(f.ctx, aiInspector, wo, true, ""); err == nil {
		t.Fatal("an AI principal certified maintenance work")
	}
	// The certifier sends it back once: the report is cleared and the work is open again.
	if err := f.svc.CloseWorkOrder(f.ctx, fw.inspector, wo, false, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("sent back without a reason: %v", err)
	}
	must(t, "send back", f.svc.CloseWorkOrder(f.ctx, fw.inspector, wo, false, "Engine still hunts at idle; bleed the injection pump"))
	if _, err := f.svc.MoveEquipment(f.ctx, fw.inspector, eq, "IN_SERVICE", "Started fine", ""); err == nil {
		t.Fatal("a machine returned to service after its work was sent back")
	}
	must(t, "complete again", f.svc.CompleteWorkOrder(f.ctx, officer2, wo, "Injection pump bled; idle steady for 20 minutes", dec("1")))
	must(t, "close", f.svc.CloseWorkOrder(f.ctx, inspector2, wo, true, ""))
	if err := f.svc.CloseWorkOrder(f.ctx, fw.inspector, wo, true, ""); err == nil {
		t.Fatal("a closed work order was closed again")
	}

	// Now, and only now, the machine goes back to work at its project.
	got, err := f.svc.MoveEquipment(f.ctx, fw.inspector, eq, "IN_SERVICE", "Work order closed; started and loaded normally", "")
	if err != nil || got != "ASSIGNED" {
		t.Fatalf("return to service after the close: %s %v", got, err)
	}
	// A later spell of maintenance needs its own closed work order: the old one does not count.
	_, err = f.svc.MoveEquipment(f.ctx, c.siteEng, eq, "UNDER_MAINTENANCE", "Track adjustment", "")
	must(t, "second maintenance", err)
	if _, err := f.svc.MoveEquipment(f.ctx, fw.inspector, eq, "IN_SERVICE", "Adjusted", ""); err == nil {
		t.Fatal("an earlier work order released the machine from a later spell of maintenance")
	}
	fw.closedWorkOrder(t, eq, "Track tension adjusted on both sides")
	if _, err := f.svc.MoveEquipment(f.ctx, fw.inspector, eq, "IN_SERVICE", "Adjusted and checked", ""); err != nil {
		t.Fatalf("return to service after the second work order: %v", err)
	}

	// History: the work orders with their parts cost, and the hours out of service.
	h, err := f.svc.EquipmentMaintenanceOf(f.ctx, c.pm, eq)
	must(t, "history", err)
	if len(h.WorkOrders) != 2 || h.WorkOrders[1].Number == "" || h.WorkOrders[1].Status != "CLOSED" || h.WorkOrders[1].Parts != 1 || !h.WorkOrders[1].PartsCost.Equal(dec("1900")) ||
		h.WorkOrders[1].LabourHours == nil || !h.WorkOrders[1].LabourHours.Equal(dec("1")) {
		t.Fatalf("history: %+v", h.WorkOrders)
	}
	for _, st := range []string{"BREAKDOWN", "UNDER_MAINTENANCE", "UNDER_INSPECTION"} {
		if _, ok := h.DownHours[st]; !ok {
			t.Fatalf("no out-of-service time recorded for %s: %v", st, h.DownHours)
		}
	}

	// A tenant database login cannot write around the kernel.
	draft, err := f.svc.CreateWorkOrder(f.ctx, fw.officer, WorkOrderInput{EquipmentID: eq, Kind: "INSPECTION", Description: "Annual lifting gear inspection"})
	must(t, "draft", err)
	for name, sql := range map[string]string{
		"authorise a draft directly":     `UPDATE maintenance_work_orders SET status = 'APPROVED', approved_by = 'u-plant-manager' WHERE id = '` + draft + `'`,
		"close a draft directly":         `UPDATE maintenance_work_orders SET status = 'CLOSED', closed_by = 'u-plant-inspector' WHERE id = '` + draft + `'`,
		"insert a closed work order":     `INSERT INTO maintenance_work_orders (equipment_id, number, kind, description, created_by, status, closed_by, closed_at) VALUES ('` + eq + `', 'MWO-FORGED', 'CORRECTIVE', 'forged closed order', 'u-x', 'CLOSED', 'u-y', now())`,
		"edit a closed work order":       `UPDATE maintenance_work_orders SET work_done = 'something else entirely' WHERE id = '` + wo + `'`,
		"delete a work order":            `DELETE FROM maintenance_work_orders WHERE id = '` + draft + `'`,
		"link any stock issue as a part": `INSERT INTO maintenance_parts (work_order_id, stock_issue_id, created_by) VALUES ('` + wo + `', '` + issue + `', 'u-storekeeper')`,
		"delete a part":                  `DELETE FROM maintenance_parts WHERE stock_issue_id = '` + issue + `'`,
		"a plan without an attestation":  `INSERT INTO maintenance_plans (equipment_id, title, every_days, created_by) VALUES ('` + eq + `', 'Forged plan', 30, 'u-plant-officer')`,
	} {
		if err := w.rawExec(sql); err == nil {
			t.Errorf("OPEN: raw SQL as the tenant login: %s", name)
		}
	}
	// Another tenant, and staff of another project.
	if _, err := f.svc.EquipmentMaintenanceOf(f.ctx, f.outsider, eq); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant read the maintenance history: %v", err)
	}
	if _, err := f.svc.WorkOrderParts(f.ctx, f.outsider, wo); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant read the parts: %v", err)
	}
	if _, err := f.svc.WorkOrderParts(f.ctx, elsewhere, wo); !errors.Is(err, ErrNotFound) && !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("staff of another project read the parts: %v", err)
	}
	if err := f.svc.CancelWorkOrder(f.ctx, officer2, draft, "Not mine to cancel"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("someone else cancelled the draft: %v", err)
	}
	must(t, "cancel draft", f.svc.CancelWorkOrder(f.ctx, fw.officer, draft, "Raised twice by mistake"))
}

// TestMaintenance_PlansAndDue: a preventive plan counts from the meter and the
// date at which it was made, shows what is due, is raised as a work order once
// at a time, and moves on only when that work order is closed.
func TestMaintenance_PlansAndDue(t *testing.T) {
	w := newFuelWorld(t)
	f, c, fw := w.f, w.cast, w.fw
	eq := w.working(t, "M-200") // meter 1000
	fw.authorise(t, "op-abebe")
	days := 90
	every := dec("250")
	if _, err := f.svc.CreateMaintenancePlan(f.ctx, c.siteEng, MaintenancePlanInput{EquipmentID: eq, Title: "250 hour service", EveryUnits: &every}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a plan made without fleet.manage: %v", err)
	}
	if _, err := f.svc.CreateMaintenancePlan(f.ctx, fw.officer, MaintenancePlanInput{EquipmentID: eq, Title: "No interval"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a plan without an interval: %v", err)
	}
	plan, err := f.svc.CreateMaintenancePlan(f.ctx, fw.officer, MaintenancePlanInput{EquipmentID: eq, Title: "250 hour service", EveryUnits: &every, EveryDays: &days})
	must(t, "plan", err)
	if _, err := f.svc.CreateMaintenancePlan(f.ctx, fw.officer, MaintenancePlanInput{EquipmentID: eq, Title: "250 hour service", EveryUnits: &every}); err == nil {
		t.Fatal("the same plan was made twice for one machine")
	}
	due := func(by auth.Principal) DuePlan {
		t.Helper()
		list, err := f.svc.MaintenanceDue(f.ctx, by)
		must(t, "due", err)
		if len(list) != 1 {
			t.Fatalf("due list: %+v", list)
		}
		return list[0]
	}
	if d := due(c.pm); d.State != fleet.DueOK || d.UnitsLeft == nil || !d.UnitsLeft.Equal(dec("250")) || d.DaysLeft == nil || *d.DaysLeft != 90 || !d.LastDoneMeter.Equal(dec("1000")) {
		t.Fatalf("a new plan: %+v", d)
	}
	// The meter advances only through approved logs. 16 h a day for 15 days = 240 h: ten hours left, due soon.
	for i := 15; i >= 1; i-- {
		open := dec("1000").Add(dec("16").Mul(dec("15").Sub(decInt(i))))
		log, err := f.svc.CreateEquipmentLog(f.ctx, c.siteEng, EquipmentLogInput{EquipmentID: eq, ProjectID: f.project, LogDate: dayOff(-i), Operator: "op-abebe",
			MeterOpen: open, MeterClose: open.Add(dec("16")), ProductiveHours: dec("14"), IdleHours: dec("2"), CostCodeID: w.costCode})
		must(t, "log", err)
		must(t, "submit log", f.svc.SubmitEquipmentLog(f.ctx, c.siteEng, log))
		must(t, "approve log", f.svc.DecideEquipmentLog(f.ctx, c.pm, log, true, ""))
	}
	if d := due(c.pm); d.State != fleet.DueSoon || !d.UnitsLeft.Equal(dec("10")) || !d.Meter.Equal(dec("1240")) {
		t.Fatalf("after 240 hours: %+v", d)
	}
	// Raised as a preventive work order, once.
	if _, err := f.svc.CreateWorkOrder(f.ctx, fw.officer, WorkOrderInput{EquipmentID: eq, PlanID: plan, Kind: "CORRECTIVE", Description: "250 hour service: oils and filters"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a corrective order carrying out a plan: %v", err)
	}
	other := fw.machine(t, "M-201")
	if _, err := f.svc.CreateWorkOrder(f.ctx, fw.officer, WorkOrderInput{EquipmentID: other, PlanID: plan, Kind: "PREVENTIVE", Description: "250 hour service: oils and filters"}); err == nil {
		t.Fatal("a plan of one machine was carried out on another")
	}
	// FLT-03.1.d (0092): the calendar. Approved logs ran 240 h over the last 30 days (8 a day), 10 h are
	// left: the meter-based service is projected 2 days out, labelled an estimate; the 90-day interval
	// falls 90 days after the plan was made; a work order cannot be planned in the past.
	day := func(n int) string { return time.Now().UTC().AddDate(0, 0, n).Format("2006-01-02") }
	cal, err := f.svc.MaintenanceCalendar(f.ctx, c.pm, time.Now().UTC().Truncate(24*time.Hour), time.Now().UTC().AddDate(0, 0, 100))
	must(t, "calendar", err)
	kinds := map[string]string{}
	for _, e := range cal {
		kinds[e.Kind] = e.Date
		if e.Kind == "PLAN_PROJECTED" && !strings.HasPrefix(e.Basis, "estimate:") {
			t.Fatalf("a projected service is not labelled an estimate: %+v", e)
		}
	}
	if len(cal) != 2 || kinds["PLAN_PROJECTED"] != day(2) || kinds["PLAN_DUE"] != day(90) {
		t.Fatalf("calendar: %+v", cal)
	}
	if _, err := f.svc.CreateWorkOrder(f.ctx, fw.officer, WorkOrderInput{EquipmentID: eq, Kind: "INSPECTION", Description: "Inspection planned yesterday", PlannedFor: day(-1)}); err == nil {
		t.Fatal("a work order was planned in the past")
	}
	wo, err := f.svc.CreateWorkOrder(f.ctx, fw.officer, WorkOrderInput{EquipmentID: eq, PlanID: plan, Kind: "PREVENTIVE", Description: "250 hour service: oils and filters", PlannedFor: day(1)})
	must(t, "preventive work order", err)
	cal, err = f.svc.MaintenanceCalendar(f.ctx, c.pm, time.Now().UTC().Truncate(24*time.Hour), time.Now().UTC().AddDate(0, 0, 100))
	must(t, "calendar 2", err)
	if len(cal) != 3 || cal[0].Kind != "WORK_ORDER" || cal[0].Date != day(1) || cal[1].Reference != wo {
		t.Fatalf("calendar with the planned work order: %+v", cal)
	}
	if err := w.rawExec(`UPDATE maintenance_work_orders SET planned_for = current_date + 30 WHERE id = $1::uuid`, wo); err == nil {
		t.Fatal("a work order's planned day was moved by raw SQL")
	}
	if list, err := f.svc.MaintenanceCalendar(f.ctx, f.outsider, time.Now().UTC(), time.Now().UTC().AddDate(0, 0, 100)); err == nil && len(list) != 0 {
		t.Fatalf("another tenant sees the calendar: %+v", list)
	}
	if _, err := f.svc.CreateWorkOrder(f.ctx, fw.officer, WorkOrderInput{EquipmentID: eq, PlanID: plan, Kind: "PREVENTIVE", Description: "250 hour service: oils and filters"}); err == nil {
		t.Fatal("a due service was raised twice")
	}
	if d := due(c.pm); d.OpenWorkOrder != wo {
		t.Fatalf("the due list does not show the open work order: %+v", d)
	}
	// Only the certified close moves the plan on; raw SQL cannot.
	if err := w.rawExec(`UPDATE maintenance_plans SET last_done_meter = 5000 WHERE id = $1::uuid`, plan); err == nil {
		t.Error("OPEN: a plan was marked done by raw SQL")
	}
	if err := w.rawExec(`UPDATE maintenance_plans SET every_units = 100000 WHERE id = $1::uuid`, plan); err == nil {
		t.Error("OPEN: a plan's interval was changed by raw SQL")
	}
	must(t, "submit", f.svc.SubmitWorkOrder(f.ctx, fw.officer, wo))
	must(t, "authorise", f.svc.DecideWorkOrder(f.ctx, fw.manager, wo, true, ""))
	must(t, "complete", f.svc.CompleteWorkOrder(f.ctx, fw.officer, wo, "Engine oil, hydraulic return and fuel filters changed", dec("4")))
	if d := due(c.pm); !d.LastDoneMeter.Equal(dec("1000")) {
		t.Fatalf("the plan moved on before its work order was certified: %+v", d)
	}
	must(t, "close", f.svc.CloseWorkOrder(f.ctx, fw.inspector, wo, true, ""))
	if d := due(c.pm); d.State != fleet.DueOK || !d.LastDoneMeter.Equal(dec("1240")) || !d.UnitsLeft.Equal(dec("250")) || *d.DaysLeft != 90 || d.OpenWorkOrder != "" {
		t.Fatalf("after the certified service: %+v", d)
	}
	// Scope: staff of another project and another tenant see no plans.
	elsewhere := auth.Principal{UserID: "u-eng-9", TenantID: f.tenant, Roles: []string{"site_engineer"}, ProjectIDs: []string{"00000000-0000-4000-8000-000000000009"}, IssuedAt: time.Now().Add(-time.Minute), Kind: auth.KindHuman}
	for name, by := range map[string]auth.Principal{"another project": elsewhere, "another tenant": f.outsider} {
		if list, err := f.svc.MaintenanceDue(f.ctx, by); err != nil || len(list) != 0 {
			t.Fatalf("%s sees %d plans: %v", name, len(list), err)
		}
	}
	// The record of the work order shows who did what.
	var by string
	must(t, "who", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT created_by || '>' || approved_by || '>' || completed_by || '>' || closed_by FROM maintenance_work_orders WHERE id = $1::uuid`, wo).Scan(&by)
	}))
	if by != strings.Join([]string{fw.officer.UserID, fw.manager.UserID, fw.officer.UserID, fw.inspector.UserID}, ">") {
		t.Fatalf("hands on the work order: %s", by)
	}
}

func decInt(n int) decimal.Decimal { return decimal.NewFromInt(int64(n)) }
