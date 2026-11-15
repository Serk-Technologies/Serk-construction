// Fleet corrections after independent review (migration 0057, ADR 0024).
package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/fleet"
)

// TestMaintenance_SeparationAsEnforced states exactly what separation the
// system enforces on a work order, at the kernel and again at the database:
//
//   - the authoriser is not the preparer;
//   - the certifier is none of the preparer, the authoriser or the reporter;
//   - authorising and certifying each need a fresh second factor, checked by
//     the database as well as the application;
//   - the preparer may also be the reporter (plant staff raise and do their
//     own work), so a work order involves at least three people, not four.
func TestMaintenance_SeparationAsEnforced(t *testing.T) {
	w := newFuelWorld(t)
	f, c, fw := w.f, w.cast, w.fw
	eq := w.working(t, "S-100")
	now := time.Now()
	multi := func(id string, roles ...string) auth.Principal {
		return auth.Principal{UserID: id, TenantID: f.tenant, Roles: roles, IssuedAt: now.Add(-time.Minute), Kind: auth.KindHuman, MFA: true, AuthTime: now}
	}
	open := func(by auth.Principal, what string) string {
		t.Helper()
		id, err := f.svc.CreateWorkOrder(f.ctx, by, WorkOrderInput{EquipmentID: eq, Kind: "CORRECTIVE", Description: what})
		must(t, "work order", err)
		must(t, "submit", f.svc.SubmitWorkOrder(f.ctx, by, id))
		return id
	}

	// 1. The preparer never authorises, whatever roles they hold.
	officerManager := multi("u-officer-manager", "plant_officer", "plant_manager")
	wo1 := open(officerManager, "Swing motor leaking at the case drain")
	if err := f.svc.DecideWorkOrder(f.ctx, officerManager, wo1, true, ""); err == nil {
		t.Fatal("the preparer authorised their own work order")
	}
	must(t, "authorise", f.svc.DecideWorkOrder(f.ctx, fw.manager, wo1, true, ""))

	// 2. The preparer may report the work complete (three people is the minimum)...
	must(t, "complete by the preparer", f.svc.CompleteWorkOrder(f.ctx, officerManager, wo1, "Case drain seal replaced; no leak after one hour", dec("2")))
	// ...but the certifier is none of the earlier hands.
	for name, by := range map[string]auth.Principal{
		"the preparer and reporter": multi(officerManager.UserID, "plant_inspector"),
		"the authoriser":            multi(fw.manager.UserID, "plant_inspector"),
	} {
		if err := f.svc.CloseWorkOrder(f.ctx, by, wo1, true, ""); err == nil {
			t.Fatalf("%s certified the work order", name)
		}
		if err := f.svc.CloseWorkOrder(f.ctx, by, wo1, false, "Sent back by someone who may not certify it"); err == nil {
			t.Fatalf("%s sent the work order back", name)
		}
	}

	// 3. The same rules at the database: attestations genuinely signed for the wrong person or without a fresh
	//    second factor do not get the row through. (The kernel is bypassed on purpose here.)
	direct := func(by auth.Principal, action, id, actx, sql string, args ...any) error {
		return f.svc.store.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			if err := f.svc.authorize(ctx, tx, by, action, "maintenance_work_order", id, actx); err != nil {
				t.Fatalf("issuing the attestation for the direct write: %v", err)
			}
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
	}
	authoriserAsCertifier := multi(fw.manager.UserID, "plant_inspector", "plant_manager")
	if err := direct(authoriserAsCertifier, "fleet.maintenance.close", wo1, "CLOSED", `UPDATE maintenance_work_orders SET status = 'CLOSED', closed_by = $2 WHERE id = $1::uuid`, wo1, authoriserAsCertifier.UserID); err == nil || !strings.Contains(err.Error(), "did not prepare, authorise or report") {
		t.Fatalf("database let the authoriser certify: %v", err)
	}
	preparerAsCertifier := multi(officerManager.UserID, "plant_inspector")
	if err := direct(preparerAsCertifier, "fleet.maintenance.close", wo1, "CLOSED", `UPDATE maintenance_work_orders SET status = 'CLOSED', closed_by = $2 WHERE id = $1::uuid`, wo1, preparerAsCertifier.UserID); err == nil {
		t.Fatal("database let the preparer certify")
	}
	staleInspector := fw.inspector
	staleInspector.AuthTime = now.Add(-2 * time.Hour)
	if err := direct(staleInspector, "fleet.maintenance.close", wo1, "CLOSED", `UPDATE maintenance_work_orders SET status = 'CLOSED', closed_by = $2 WHERE id = $1::uuid`, wo1, staleInspector.UserID); err == nil || !strings.Contains(err.Error(), "multi-factor") {
		t.Fatalf("database closed a work order on an attestation without a fresh second factor: %v", err)
	}
	wo2 := open(fw.officer, "Travel motor noisy on the left side")
	staleManager := fw.manager
	staleManager.AuthTime = now.Add(-2 * time.Hour)
	if err := direct(staleManager, "fleet.maintenance.decide", wo2, "APPROVED", `UPDATE maintenance_work_orders SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid`, wo2, staleManager.UserID); err == nil || !strings.Contains(err.Error(), "multi-factor") {
		t.Fatalf("database authorised a repair on an attestation without a fresh second factor (the 0056 gap): %v", err)
	}
	// An attestation is for one decision on one record: it does not authorise another record, another outcome, or a second use.
	wo3 := open(fw.officer, "Bucket teeth worn to the adapters")
	if err := direct(fw.manager, "fleet.maintenance.decide", wo2, "APPROVED", `UPDATE maintenance_work_orders SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid`, wo3, fw.manager.UserID); err == nil {
		t.Fatal("an attestation for one work order authorised another")
	}
	if err := direct(fw.manager, "fleet.maintenance.decide", wo2, "REJECTED", `UPDATE maintenance_work_orders SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid`, wo2, fw.manager.UserID); err == nil {
		t.Fatal("an attestation to reject was used to approve")
	}
	if err := direct(fw.manager, "fleet.maintenance.decide", wo2, "APPROVED", `UPDATE maintenance_work_orders SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid`, wo2, "u-someone-else"); err == nil {
		t.Fatal("an attestation was used in another person's name")
	}
	must(t, "attested approval of wo2", direct(fw.manager, "fleet.maintenance.decide", wo2, "APPROVED", `UPDATE maintenance_work_orders SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid`, wo2, fw.manager.UserID))
	if err := w.rawExec(`UPDATE maintenance_work_orders SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid`, wo3, fw.manager.UserID); err == nil {
		t.Fatal("OPEN: yesterday's attestation was reused in a later transaction")
	}
	// Forged session information does not stand in for an attestation.
	if err := w.raw(func(ctx context.Context, tx pgx.Tx) error {
		for _, g := range [][2]string{{"serk.actor", fw.manager.UserID}, {"serk.mfa", "true"}, {"serk.permissions", "fleet.approve,fleet.release"}, {"request.jwt.claims", `{"sub":"` + fw.manager.UserID + `","amr":["mfa"]}`}} {
			if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, g[0], g[1]); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE maintenance_work_orders SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid`, wo3, fw.manager.UserID)
		return err
	}); err == nil {
		t.Fatal("OPEN: forged session settings authorised a work order")
	}
	// The tenant login cannot mint attestations of its own.
	if err := w.rawExec(`INSERT INTO approval_attestations SELECT * FROM approval_attestations LIMIT 1`); err == nil {
		t.Fatal("OPEN: the tenant login wrote an approval attestation")
	}

	// 4. Other projects and other tenants reach none of it.
	elsewhere := auth.Principal{UserID: "u-eng-9", TenantID: f.tenant, Roles: []string{"site_engineer", "storekeeper"}, ProjectIDs: []string{"00000000-0000-4000-8000-000000000009"}, IssuedAt: now.Add(-time.Minute), Kind: auth.KindHuman}
	if err := f.svc.SubmitWorkOrder(f.ctx, elsewhere, wo3); err == nil {
		t.Fatal("staff of another project submitted a work order")
	}
	if _, err := f.svc.RequestWorkOrderPart(f.ctx, elsewhere, wo2, WorkOrderPartInput{WarehouseID: w.store, StockItemID: w.diesel, CostCodeID: w.costCode, ExpenseAccountID: w.acct["5000"], Quantity: dec("1")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a storekeeper of another project requested a part: %v", err)
	}
	outsiderManager := f.outsider
	outsiderManager.Roles, outsiderManager.MFA, outsiderManager.AuthTime = []string{"plant_manager", "plant_inspector", "plant_officer"}, true, now
	if err := f.svc.DecideWorkOrder(f.ctx, outsiderManager, wo3, true, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant's plant manager authorised this work order: %v", err)
	}
	if err := f.svc.CloseWorkOrder(f.ctx, outsiderManager, wo1, true, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant's inspector closed this work order: %v", err)
	}

	// 5. The proper certifier closes it: three distinct people on the record.
	must(t, "close", f.svc.CloseWorkOrder(f.ctx, fw.inspector, wo1, true, ""))
	var people int
	must(t, "people", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(DISTINCT x) FROM maintenance_work_orders w, unnest(ARRAY[w.created_by, w.approved_by, w.completed_by, w.closed_by]) x WHERE w.id = $1::uuid`, wo1).Scan(&people)
	}))
	if people != 3 {
		t.Fatalf("%d distinct people on the work order, want 3 (preparer who also reported, authoriser, certifier)", people)
	}
	_ = c
}

// TestFleetPolicy_ApprovedThresholdsApply: the thresholds are a decision of two
// people with an effective date; reports say which policy they used; with none
// approved they say that the defaults are engineering defaults.
func TestFleetPolicy_ApprovedThresholdsApply(t *testing.T) {
	w := newFuelWorld(t)
	f, c, fw := w.f, w.cast, w.fw
	eq := w.working(t, "P-100")
	must(t, "designate diesel", f.svc.DesignateFuelItem(f.ctx, fw.manager, w.diesel))
	fw.authorise(t, "op-abebe")
	// Two logged days under two standards: 8 units at 14 L, then 8 units at 10 L => 192 L expected.
	for _, s := range []struct {
		rate string
		from int
	}{{"14", -60}, {"10", -1}} {
		_, err := f.svc.SetFuelStandard(f.ctx, fw.manager, FuelStandardInput{EquipmentID: eq, LitresPerUnit: dec(s.rate), EffectiveFrom: dayOff(s.from), Basis: "Synthetic test standard"})
		must(t, "standard", err)
	}
	for i, day := range []int{-3, -1} {
		open := dec("1000").Add(decInt(8 * i))
		log, err := f.svc.CreateEquipmentLog(f.ctx, c.siteEng, EquipmentLogInput{EquipmentID: eq, ProjectID: f.project, LogDate: dayOff(day), Operator: "op-abebe",
			MeterOpen: open, MeterClose: open.Add(dec("8")), ProductiveHours: dec("7"), IdleHours: dec("1"), FuelLitres: dec("100"), CostCodeID: w.costCode})
		must(t, "log", err)
		must(t, "submit log", f.svc.SubmitEquipmentLog(f.ctx, c.siteEng, log))
		must(t, "approve log", f.svc.DecideEquipmentLog(f.ctx, c.pm, log, true, ""))
	}
	d, err := f.svc.DispenseFuel(f.ctx, c.store, w.fill(eq, "205", "1016"))
	must(t, "dispense", err)
	_, err = f.svc.ApproveStockIssue(f.ctx, c.siteEng, d.StockIssueID)
	must(t, "approve issue", err)

	line := func() (FuelReconciliation, FuelLine) {
		t.Helper()
		rec, err := f.svc.FuelReconciliationOf(f.ctx, c.pm, dayOff(-7), dayOff(0), "")
		must(t, "reconciliation", err)
		if len(rec.Lines) != 1 {
			t.Fatalf("lines: %+v", rec.Lines)
		}
		return rec, rec.Lines[0]
	}
	// No policy yet: defaults, labelled. The period spans two standards and is split log by log.
	rec, l := line()
	if rec.Policy.Source != "ENGINEERING_DEFAULT" || rec.Policy.Note == "" || l.Expected == nil || !l.Expected.Equal(dec("192")) || l.StandardsUsed != 2 || !l.UnitsNoStandard.IsZero() || len(l.Flags) != 0 {
		t.Fatalf("default policy, two standards: policy %+v line %+v", rec.Policy, l)
	}

	// Preparing and approving a policy.
	strict := FleetPolicyInput{EffectiveFrom: dayOff(-30), FuelVarianceTolerance: dec("0.02"), FuelVarianceMinLitres: dec("2"), DueSoonFraction: dec("0.25"), DueSoonDays: 30, Basis: "Synthetic policy for the test environment"}
	if _, err := f.svc.CreateFleetPolicy(f.ctx, c.siteEng, strict); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a policy prepared without fleet.manage: %v", err)
	}
	for name, mut := range map[string]func(*FleetPolicyInput){
		"a tolerance of 100%":   func(p *FleetPolicyInput) { p.FuelVarianceTolerance = dec("1") },
		"a negative minimum":    func(p *FleetPolicyInput) { p.FuelVarianceMinLitres = dec("-1") },
		"a due-soon share of 0": func(p *FleetPolicyInput) { p.DueSoonFraction = dec("0") },
		"no basis":              func(p *FleetPolicyInput) { p.Basis = "because" },
		"a bad date":            func(p *FleetPolicyInput) { p.EffectiveFrom = "soon" },
	} {
		in := strict
		mut(&in)
		if _, err := f.svc.CreateFleetPolicy(f.ctx, fw.officer, in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("a policy with %s: %v", name, err)
		}
	}
	pol, err := f.svc.CreateFleetPolicy(f.ctx, fw.officer, strict)
	must(t, "policy", err)
	if _, l := line(); len(l.Flags) != 0 {
		t.Fatalf("a draft policy took effect: %+v", l.Flags)
	}
	must(t, "submit policy", f.svc.SubmitFleetPolicy(f.ctx, fw.officer, pol))
	both := fw.manager
	both.UserID, both.Roles = fw.officer.UserID, []string{"plant_manager", "plant_officer"}
	if err := f.svc.DecideFleetPolicy(f.ctx, both, pol, true, ""); err == nil {
		t.Fatal("the preparer approved their own fleet policy")
	}
	stale := fw.manager
	stale.AuthTime = time.Now().Add(-time.Hour)
	if err := f.svc.DecideFleetPolicy(f.ctx, stale, pol, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("a policy approved without fresh MFA: %v", err)
	}
	ai := fw.manager
	ai.Kind = auth.KindAI
	if err := f.svc.DecideFleetPolicy(f.ctx, ai, pol, true, ""); err == nil {
		t.Fatal("an AI principal approved a fleet policy")
	}
	if _, l := line(); len(l.Flags) != 0 {
		t.Fatalf("a submitted policy took effect: %+v", l.Flags)
	}
	must(t, "approve policy", f.svc.DecideFleetPolicy(f.ctx, fw.manager, pol, true, ""))

	// Approved and effective: 205 L against 192 L expected and 200 L logged is now inside neither threshold.
	rec, l = line()
	if rec.Policy.Source != "APPROVED_POLICY" || rec.Policy.PolicyID != pol || rec.Policy.FuelVarianceTolerance != "0.02" || rec.Policy.Number == "" {
		t.Fatalf("policy in force: %+v", rec.Policy)
	}
	codes := ""
	for _, x := range l.Flags {
		codes += x.Code + " "
	}
	if codes != "DISPENSED_NOT_LOGGED ABOVE_STANDARD " {
		t.Fatalf("flags under the approved policy: %q", codes)
	}
	// A policy that starts tomorrow does not apply to a period that ends today.
	later := strict
	later.EffectiveFrom, later.FuelVarianceTolerance = dayOff(1), dec("0.5")
	pol2, err := f.svc.CreateFleetPolicy(f.ctx, fw.officer, later)
	must(t, "later policy", err)
	must(t, "submit later", f.svc.SubmitFleetPolicy(f.ctx, fw.officer, pol2))
	must(t, "approve later", f.svc.DecideFleetPolicy(f.ctx, fw.manager, pol2, true, ""))
	if rec, _ := line(); rec.Policy.PolicyID != pol {
		t.Fatalf("a policy effective tomorrow was applied today: %+v", rec.Policy)
	}
	if in, err := f.svc.FleetPolicyInForce(f.ctx, c.pm, dayOff(2)); err != nil || in.PolicyID != pol2 {
		t.Fatalf("policy in force the day after tomorrow: %+v %v", in, err)
	}
	// One approved policy per effective date.
	dup, err := f.svc.CreateFleetPolicy(f.ctx, fw.officer, strict)
	must(t, "duplicate date", err)
	must(t, "submit duplicate", f.svc.SubmitFleetPolicy(f.ctx, fw.officer, dup))
	if err := f.svc.DecideFleetPolicy(f.ctx, fw.manager, dup, true, ""); err == nil {
		t.Fatal("two approved policies for one effective date")
	}
	// The due-soon rule follows the policy too (a quarter of the interval instead of a tenth).
	every := dec("64")
	_, err = f.svc.CreateMaintenancePlan(f.ctx, fw.officer, MaintenancePlanInput{EquipmentID: eq, Title: "Greasing round", EveryUnits: &every})
	must(t, "plan", err)
	// (the plan counts from the meter now, 1016; nothing has run since: 64 of 64 left, not due)
	if list, err := f.svc.MaintenanceDue(f.ctx, c.pm); err != nil || len(list) != 1 || list[0].State != fleet.DueOK {
		t.Fatalf("due: %+v %v", list, err)
	}
	// Raw SQL: a decided policy never changes; a policy cannot be inserted approved or approved without an attestation.
	for name, sql := range map[string]string{
		"loosen an approved policy":  `UPDATE fleet_policies SET fuel_variance_tolerance = 0.9 WHERE id = '` + pol + `'`,
		"insert an approved policy":  `INSERT INTO fleet_policies (number, effective_from, fuel_variance_tolerance, fuel_variance_min_litres, due_soon_fraction, due_soon_days, basis, created_by, status, approved_by) VALUES ('FPL-FORGED', current_date, 0.9, 0, 0.5, 1, 'forged policy row', 'u-a', 'APPROVED', 'u-b')`,
		"approve a submitted policy": `UPDATE fleet_policies SET status = 'APPROVED', approved_by = 'u-plant-manager' WHERE id = '` + dup + `'`,
		"delete a policy":            `DELETE FROM fleet_policies WHERE id = '` + pol + `'`,
	} {
		if err := w.rawExec(sql); err == nil {
			t.Errorf("OPEN: raw SQL as the tenant login: %s", name)
		}
	}
	// Another tenant has no policy of ours.
	if in, err := f.svc.FleetPolicyInForce(f.ctx, f.outsider, ""); err != nil || in.Source != "ENGINEERING_DEFAULT" {
		t.Fatalf("another tenant sees this tenant's policy: %+v %v", in, err)
	}
}

// TestMaintenance_PlanRevisionAndRetirement: a plan is revised by a new version
// that retires the old one and continues from its last service, or retired
// with a reason. Retired plans are kept, are not due and take no work orders.
func TestMaintenance_PlanRevisionAndRetirement(t *testing.T) {
	w := newFuelWorld(t)
	f, c, fw := w.f, w.cast, w.fw
	eq := w.working(t, "R-100")
	every, days := dec("250"), 90
	plan, err := f.svc.CreateMaintenancePlan(f.ctx, fw.officer, MaintenancePlanInput{EquipmentID: eq, Title: "250 hour service", EveryUnits: &every, EveryDays: &days})
	must(t, "plan", err)

	// Revision: same title allowed, new interval, old plan retired and kept.
	longer := dec("500")
	if _, err := f.svc.ReviseMaintenancePlan(f.ctx, c.siteEng, plan, MaintenancePlanInput{Title: "250 hour service", EveryUnits: &longer}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a plan revised without fleet.manage: %v", err)
	}
	wo, err := f.svc.CreateWorkOrder(f.ctx, fw.officer, WorkOrderInput{EquipmentID: eq, PlanID: plan, Kind: "PREVENTIVE", Description: "250 hour service: oils and filters"})
	must(t, "work order on the plan", err)
	if _, err := f.svc.ReviseMaintenancePlan(f.ctx, fw.officer, plan, MaintenancePlanInput{Title: "250 hour service", EveryUnits: &longer}); err == nil {
		t.Fatal("a plan with an open work order was revised")
	}
	if err := f.svc.RetireMaintenancePlan(f.ctx, fw.officer, plan, "No longer needed at this site"); err == nil {
		t.Fatal("a plan with an open work order was retired")
	}
	must(t, "cancel the work order", f.svc.CancelWorkOrder(f.ctx, fw.officer, wo, "Plan is being revised"))
	rev, err := f.svc.ReviseMaintenancePlan(f.ctx, fw.officer, plan, MaintenancePlanInput{Title: "250 hour service", EveryUnits: &longer})
	must(t, "revise", err)
	list, err := f.svc.MaintenanceDue(f.ctx, c.pm)
	must(t, "due", err)
	if len(list) != 1 || list[0].PlanID != rev || list[0].EveryUnits == nil || !list[0].EveryUnits.Equal(dec("500")) || list[0].EveryDays != nil {
		t.Fatalf("after revision the due list shows: %+v", list)
	}
	var kept, retiredBy, reason string
	must(t, "old plan", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*)::text, max(retired_by), max(retire_reason) FROM maintenance_plans WHERE id = $1::uuid AND retired_at IS NOT NULL`, plan).Scan(&kept, &retiredBy, &reason)
	}))
	if kept != "1" || retiredBy != fw.officer.UserID || reason != "Superseded by a revision" {
		t.Fatalf("the superseded plan: kept %s retired by %s reason %q", kept, retiredBy, reason)
	}
	if _, err := f.svc.ReviseMaintenancePlan(f.ctx, fw.officer, plan, MaintenancePlanInput{Title: "Again", EveryUnits: &longer}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a retired plan was revised again: %v", err)
	}
	if _, err := f.svc.CreateWorkOrder(f.ctx, fw.officer, WorkOrderInput{EquipmentID: eq, PlanID: plan, Kind: "PREVENTIVE", Description: "Service on the retired plan"}); err == nil {
		t.Fatal("a work order was raised from a retired plan")
	}

	// Retirement on its own.
	if err := f.svc.RetireMaintenancePlan(f.ctx, c.siteEng, rev, "Not our plan"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a plan retired without fleet.manage: %v", err)
	}
	if err := f.svc.RetireMaintenancePlan(f.ctx, fw.officer, rev, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("a plan retired without a reason: %v", err)
	}
	must(t, "retire", f.svc.RetireMaintenancePlan(f.ctx, fw.officer, rev, "Machine moved to a service contract"))
	if err := f.svc.RetireMaintenancePlan(f.ctx, fw.officer, rev, "Twice is once too many"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a plan retired twice: %v", err)
	}
	if list, err := f.svc.MaintenanceDue(f.ctx, c.pm); err != nil || len(list) != 0 {
		t.Fatalf("a retired plan is still due: %+v %v", list, err)
	}
	// The title is free again for a new plan; raw SQL cannot retire, un-retire or supersede.
	fresh, err := f.svc.CreateMaintenancePlan(f.ctx, fw.officer, MaintenancePlanInput{EquipmentID: eq, Title: "250 hour service", EveryDays: &days})
	must(t, "new plan under the old title", err)
	for name, sql := range map[string]string{
		"retire a plan directly":           `UPDATE maintenance_plans SET retired_at = now(), retired_by = 'u-plant-officer', retire_reason = 'direct write' WHERE id = '` + fresh + `'`,
		"bring a retired plan back":        `UPDATE maintenance_plans SET retired_at = NULL, retired_by = NULL, retire_reason = NULL WHERE id = '` + rev + `'`,
		"supersede without an attestation": `INSERT INTO maintenance_plans (equipment_id, title, every_days, created_by, supersedes) VALUES ('` + eq + `', 'Forged revision', 10, 'u-plant-officer', '` + fresh + `')`,
	} {
		if err := w.rawExec(sql); err == nil {
			t.Errorf("OPEN: raw SQL as the tenant login: %s", name)
		}
	}
	if err := f.svc.RetireMaintenancePlan(f.ctx, f.outsider, fresh, "Another tenant's reason"); err == nil {
		t.Fatal("another tenant retired this plan")
	}
}
