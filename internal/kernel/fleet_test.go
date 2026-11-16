package kernel

// Tests for SERK FLEET part 1 (requirements FLT-01.x; migration 0054).
// Proven: the Go and SQL state machines agree on all 196 pairs of states; a
// machine is registered once; commissioning needs a second person with fresh
// MFA; a held, broken or maintained machine returns to service only through a
// certifier; a machine reaches a project only through an approved reservation
// of that project; approved reservations of one machine never overlap, also
// when sixteen are approved at once; history is written by the database and
// cannot be edited; daily logs are unique per machine, day and shift, name an
// authorised operator, fit the meter and the day; the meter never runs
// backwards and moves only through an approved log or reset; an offline
// client's repeat is recorded once; other projects and tenants see nothing
// they should not; usage figures equal the approved logs.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/fleet"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

type fleetWorld struct {
	*docWorld
	officer, manager, inspector, otherEng auth.Principal
	project2                              string
}

func newFleetWorld(t *testing.T) *fleetWorld {
	t.Helper()
	w := &fleetWorld{docWorld: newDocWorld(t)}
	f := w.f
	now := time.Now()
	wide := func(id, role string, fresh bool) auth.Principal {
		p := auth.Principal{UserID: id, TenantID: f.tenant, Roles: []string{role}, IssuedAt: now.Add(-time.Minute), Kind: auth.KindHuman}
		if fresh {
			p.MFA, p.AuthTime = true, now
		}
		return p
	}
	w.officer, w.manager, w.inspector = wide("u-plant-officer", "plant_officer", false), wide("u-plant-manager", "plant_manager", true), wide("u-plant-inspector", "plant_inspector", true)
	must(t, "second project", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		w.project2, err = postgres.CreateProject(ctx, tx, f.tenant, f.entity, "P-002", "Adigrat Bypass", "ETB")
		return err
	}))
	w.otherEng = auth.Principal{UserID: "u-eng-2", TenantID: f.tenant, Roles: []string{"site_engineer", "project_manager"}, ProjectIDs: []string{w.project2}, IssuedAt: now.Add(-time.Minute), Kind: auth.KindHuman, MFA: true, AuthTime: now}
	return w
}

func dayOff(offset int) string { return time.Now().AddDate(0, 0, offset).Format("2006-01-02") }

func (w *fleetWorld) machine(t *testing.T, serial string) string {
	t.Helper()
	id, err := w.f.svc.RegisterEquipment(w.f.ctx, w.officer, EquipmentInput{LegalEntityID: w.f.entity, Name: "Excavator " + serial, Class: "EXCAVATOR", Ownership: "OWNED",
		Manufacturer: "Komatsu", Model: "PC200-8", SerialNumber: serial, OpeningMeter: dec("1000")})
	must(t, "register "+serial, err)
	_, err = w.f.svc.MoveEquipment(w.f.ctx, w.manager, id, "AVAILABLE", "Commissioned after inspection", "")
	must(t, "commission "+serial, err)
	return id
}

// reserve requests, submits and approves a reservation of the first project.
func (w *fleetWorld) reserve(t *testing.T, eq, project string, by auth.Principal, from, to int) string {
	t.Helper()
	id, err := w.f.svc.CreateEquipmentReservation(w.f.ctx, by, EquipmentReservationInput{EquipmentID: eq, ProjectID: project, Purpose: "Bulk excavation", StartsOn: dayOff(from), EndsOn: dayOff(to)})
	must(t, "reservation", err)
	must(t, "submit reservation", w.f.svc.SubmitEquipmentReservation(w.f.ctx, by, id))
	must(t, "approve reservation", w.f.svc.DecideEquipmentReservation(w.f.ctx, w.manager, id, true, ""))
	return id
}

// closedWorkOrder runs a corrective work order to its certified close:
// prepared and reported by the plant officer, authorised by the plant manager,
// closed by the inspector.
func (w *fleetWorld) closedWorkOrder(t *testing.T, eq, what string) string {
	t.Helper()
	f := w.f
	id, err := f.svc.CreateWorkOrder(f.ctx, w.officer, WorkOrderInput{EquipmentID: eq, Kind: "CORRECTIVE", Description: what})
	must(t, "work order", err)
	must(t, "submit work order", f.svc.SubmitWorkOrder(f.ctx, w.officer, id))
	must(t, "authorise work order", f.svc.DecideWorkOrder(f.ctx, w.manager, id, true, ""))
	must(t, "complete work order", f.svc.CompleteWorkOrder(f.ctx, w.officer, id, what+"; tested under load", dec("3.5")))
	must(t, "close work order", f.svc.CloseWorkOrder(f.ctx, w.inspector, id, true, ""))
	return id
}

func (w *fleetWorld) status(t *testing.T, eq string) string {
	t.Helper()
	e, err := w.f.svc.GetEquipment(w.f.ctx, w.officer, eq)
	must(t, "get equipment", err)
	return e.Status
}

func (w *fleetWorld) authorise(t *testing.T, operator string) {
	t.Helper()
	id, err := w.f.svc.CreateOperatorAuth(w.f.ctx, w.officer, OperatorAuthInput{Operator: operator, EquipmentClass: "EXCAVATOR", CertificateRef: "TVET/EXC/2291", ValidFrom: dayOff(-30), ValidUntil: dayOff(300)})
	must(t, "operator auth", err)
	must(t, "submit operator auth", w.f.svc.SubmitOperatorAuth(w.f.ctx, w.officer, id))
	must(t, "approve operator auth", w.f.svc.DecideOperatorAuth(w.f.ctx, w.inspector, id, true, ""))
}

func TestFleet_StateMachineGoAndSQLAgree(t *testing.T) {
	w := newFleetWorld(t)
	f := w.f
	must(t, "matrix", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		n := 0
		for _, from := range fleet.States {
			for _, to := range fleet.States {
				var sqlPerm *string
				if err := tx.QueryRow(ctx, `SELECT equipment_transition_perm($1, $2)`, from, to).Scan(&sqlPerm); err != nil {
					return err
				}
				goPerm, ok := fleet.TransitionPerm(from, to)
				if ok != (sqlPerm != nil) || ok && goPerm != *sqlPerm {
					return fmt.Errorf("%s -> %s: Go says (%q, %v), SQL says %v", from, to, goPerm, ok, sqlPerm)
				}
				n++
			}
		}
		if n != 196 {
			return fmt.Errorf("compared %d pairs", n)
		}
		return nil
	}))
}

func TestFleet_RegisterAndLifecycle(t *testing.T) {
	w := newFleetWorld(t)
	f := w.f
	in := EquipmentInput{LegalEntityID: f.entity, Name: "Excavator 01", Class: "excavator", Ownership: "owned", Manufacturer: "Komatsu", Model: "PC200-8",
		SerialNumber: "KMT-PC200-30417", Registration: "ET 3-45678", OpeningMeter: dec("1250.5"), Capacity: "0.8 m3 bucket"}
	if _, err := f.svc.RegisterEquipment(f.ctx, w.eng, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a site engineer registered equipment: %v", err)
	}
	bad := in
	bad.Class = "SPACESHIP"
	if _, err := f.svc.RegisterEquipment(f.ctx, w.officer, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown class: %v", err)
	}
	id, err := f.svc.RegisterEquipment(f.ctx, w.officer, in)
	must(t, "register", err)
	// The same machine, written differently, is still the same machine.
	dup := in
	dup.SerialNumber, dup.Registration, dup.Manufacturer = "kmt pc200 30417", "", "KOMATSU"
	if _, err := f.svc.RegisterEquipment(f.ctx, w.officer, dup); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a duplicate serial number was registered: %v", err)
	}
	dup = in
	dup.SerialNumber, dup.Registration = "OTHER-1", "et-3 45678"
	if _, err := f.svc.RegisterEquipment(f.ctx, w.officer, dup); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a duplicate registration was registered: %v", err)
	}
	rawFails(t, f, "change a serial number", `UPDATE equipment SET serial_number = 'X' WHERE id = '`+id+`'`)
	rawFails(t, f, "register as AVAILABLE", `INSERT INTO equipment (legal_entity_id, number, name, class, ownership, manufacturer, model, serial_number, status, status_changed_by, created_by)
		VALUES ('`+f.entity+`', 'EQ-X', 'Forged', 'TOOL', 'OWNED', 'm', 'm', 's-forged', 'AVAILABLE', 'u-forger', 'u-forger')`)

	// Commissioning: a second person, fresh MFA, human, attested.
	both := w.officer
	both.Roles = []string{"plant_officer", "plant_manager"}
	both.MFA, both.AuthTime = true, time.Now()
	if _, err := f.svc.MoveEquipment(f.ctx, both, id, "AVAILABLE", "Commissioned", ""); err == nil {
		t.Fatal("the registrar commissioned their own entry")
	}
	stale := w.manager
	stale.AuthTime = time.Now().Add(-time.Hour)
	if _, err := f.svc.MoveEquipment(f.ctx, stale, id, "AVAILABLE", "Commissioned", ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("commissioning without fresh MFA: %v", err)
	}
	ai := w.manager
	ai.Kind = auth.KindAI
	if _, err := f.svc.MoveEquipment(f.ctx, ai, id, "AVAILABLE", "Commissioned", ""); err == nil {
		t.Fatal("an AI principal commissioned equipment")
	}
	if _, err := f.svc.MoveEquipment(f.ctx, w.officer, id, "MOBILIZING", "Sent to site", f.project); !errors.Is(err, ledger.ErrBadTransition) {
		t.Fatalf("unregistered equipment was mobilised: %v", err)
	}
	rawFails(t, f, "commission by SQL", `UPDATE equipment SET status = 'AVAILABLE', status_reason = 'forged', status_changed_by = 'u-forger' WHERE id = '`+id+`'`)
	if _, err := f.svc.MoveEquipment(f.ctx, w.manager, id, "AVAILABLE", "Commissioned after pre-delivery inspection", ""); err != nil {
		t.Fatalf("commission: %v", err)
	}

	// To a project: only against that project's approved reservation.
	if _, err := f.svc.MoveEquipment(f.ctx, w.officer, id, "MOBILIZING", "Sent to site", f.project); err == nil {
		t.Fatal("equipment was mobilised without a reservation")
	}
	res := w.reserve(t, id, f.project, w.eng, 0, 20)
	if w.status(t, id) != "RESERVED" {
		t.Fatalf("after an approved reservation: %s", w.status(t, id))
	}
	if _, err := f.svc.MoveEquipment(f.ctx, w.officer, id, "MOBILIZING", "Sent to the wrong site", w.project2); err == nil {
		t.Fatal("equipment was mobilised to a project that did not reserve it")
	}
	if _, err := f.svc.MoveEquipment(f.ctx, w.eng, id, "MOBILIZING", "Fetched by site", f.project); !errors.Is(err, ErrNotFound) {
		t.Fatalf("project staff moved equipment out of the yard: %v", err)
	}
	if _, err := f.svc.MoveEquipment(f.ctx, w.officer, id, "MOBILIZING", "Low-bed dispatched", f.project); err != nil {
		t.Fatalf("mobilise: %v", err)
	}
	// At the project, site staff of that project move it; other projects cannot.
	if _, err := f.svc.MoveEquipment(f.ctx, w.otherEng, id, "ASSIGNED", "Arrived", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another project's engineer received the machine: %v", err)
	}
	for _, step := range []string{"ASSIGNED", "OPERATING", "IDLE", "OPERATING"} {
		if _, err := f.svc.MoveEquipment(f.ctx, w.eng, id, step, "Shift progress", ""); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}
	rawFails(t, f, "move a machine to another project by SQL", `UPDATE equipment SET current_project_id = '`+w.project2+`' WHERE id = '`+id+`'`)
	if err := f.svc.CancelEquipmentReservation(f.ctx, w.eng, res, "No longer needed"); err == nil {
		t.Fatal("the reservation of a machine at the project was cancelled")
	}

	// Stop: anyone who operates, at once. Out of a hold: only a certifier, fresh MFA.
	if _, err := f.svc.MoveEquipment(f.ctx, w.eng, id, "SAFETY_HOLD", "", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a hold without a reason: %v", err)
	}
	if _, err := f.svc.MoveEquipment(f.ctx, w.eng, id, "SAFETY_HOLD", "Hydraulic hose leaking near the cab", ""); err != nil {
		t.Fatalf("safety hold: %v", err)
	}
	for _, to := range []string{"OPERATING", "ASSIGNED", "IDLE", "IN_SERVICE", "DEMOBILIZING", "AVAILABLE"} {
		if _, err := f.svc.MoveEquipment(f.ctx, w.eng, id, to, "Back to work", ""); err == nil {
			t.Fatalf("site staff moved a held machine to %s", to)
		}
		if _, err := f.svc.MoveEquipment(f.ctx, w.officer, id, to, "Back to work", ""); err == nil {
			t.Fatalf("the plant officer moved a held machine to %s", to)
		}
	}
	rawFails(t, f, "release a hold by SQL", `UPDATE equipment SET status = 'ASSIGNED', status_reason = 'forged', status_changed_by = 'u-forger' WHERE id = '`+id+`'`)
	if _, err := f.svc.CreateEquipmentReservation(f.ctx, w.otherEng, EquipmentReservationInput{EquipmentID: id, ProjectID: w.project2, Purpose: "Next job", StartsOn: dayOff(30), EndsOn: dayOff(40)}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a held machine was reserved: %v", err)
	}
	if _, err := f.svc.MoveEquipment(f.ctx, w.eng, id, "UNDER_MAINTENANCE", "Hose replaced by the site mechanic", ""); err != nil {
		t.Fatalf("to maintenance: %v", err)
	}
	staleInspector := w.inspector
	staleInspector.AuthTime = time.Now().Add(-time.Hour)
	if _, err := f.svc.MoveEquipment(f.ctx, staleInspector, id, "IN_SERVICE", "Pressure test passed", ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("certified without fresh MFA: %v", err)
	}
	aiInspector := w.inspector
	aiInspector.Kind = auth.KindAI
	if _, err := f.svc.MoveEquipment(f.ctx, aiInspector, id, "IN_SERVICE", "Pressure test passed", ""); err == nil {
		t.Fatal("an AI principal certified equipment fit for service")
	}
	// 0056: a machine that has been under maintenance returns to service only after a closed work order.
	if _, err := f.svc.MoveEquipment(f.ctx, w.inspector, id, "IN_SERVICE", "Hose replaced, pressure test passed, no leaks", ""); err == nil {
		t.Fatal("a machine left maintenance without a closed work order")
	}
	w.closedWorkOrder(t, id, "Hydraulic hose on the boom replaced and pressure tested")
	got, err := f.svc.MoveEquipment(f.ctx, w.inspector, id, "IN_SERVICE", "Hose replaced, pressure test passed, no leaks", "")
	if err != nil || got != "ASSIGNED" {
		t.Fatalf("return to service: %s %v", got, err)
	}

	// Leaving: demobilise, back to the yard as RESERVED (the reservation runs on), cancel, AVAILABLE.
	if _, err := f.svc.MoveEquipment(f.ctx, w.eng, id, "DEMOBILIZING", "Excavation complete", ""); err != nil {
		t.Fatalf("demobilise: %v", err)
	}
	if got, err := f.svc.MoveEquipment(f.ctx, w.officer, id, "IN_SERVICE", "Back in the yard", ""); err != nil || got != "RESERVED" {
		t.Fatalf("back to the yard: %s %v", got, err)
	}
	must(t, "cancel reservation", f.svc.CancelEquipmentReservation(f.ctx, w.eng, res, "Excavation finished early"))
	if w.status(t, id) != "AVAILABLE" {
		t.Fatalf("after cancelling the reservation: %s", w.status(t, id))
	}

	// History: every move, in order, written by the database, not editable.
	e, err := f.svc.GetEquipment(f.ctx, w.eng, id)
	must(t, "detail", err)
	var trail []string
	for i := len(e.History) - 1; i >= 0; i-- {
		trail = append(trail, e.History[i].To)
	}
	want := []string{"REGISTERED", "AVAILABLE", "RESERVED", "MOBILIZING", "ASSIGNED", "OPERATING", "IDLE", "OPERATING", "SAFETY_HOLD", "UNDER_MAINTENANCE", "ASSIGNED", "DEMOBILIZING", "RESERVED", "AVAILABLE"}
	if fmt.Sprint(trail) != fmt.Sprint(want) {
		t.Fatalf("history:\n%v\nwant\n%v", trail, want)
	}
	if e.History[len(e.History)-9].Actor != w.eng.UserID || e.History[0].Actor != "system" || e.History[len(e.History)-11].Actor != w.inspector.UserID {
		t.Fatalf("history actors: %+v", e.History)
	}
	rawFails(t, f, "rewrite history", `UPDATE equipment_events SET reason = 'changed' WHERE equipment_id = '`+id+`'`)
	rawFails(t, f, "delete history", `DELETE FROM equipment_events WHERE equipment_id = '`+id+`'`)
	rawFails(t, f, "add history", `INSERT INTO equipment_events (equipment_id, to_status, actor, meter) VALUES ('`+id+`', 'OPERATING', 'u-forger', 0)`)

	// Retirement and disposal: the plant manager, fresh MFA; nothing afterwards.
	if _, err := f.svc.MoveEquipment(f.ctx, w.officer, id, "RETIRED", "Beyond economic repair", ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the officer retired equipment: %v", err)
	}
	for _, to := range []string{"RETIRED", "DISPOSED"} {
		if _, err := f.svc.MoveEquipment(f.ctx, w.manager, id, to, "Beyond economic repair; sold as scrap", ""); err != nil {
			t.Fatalf("%s: %v", to, err)
		}
	}
	for _, to := range fleet.States {
		if _, err := f.svc.MoveEquipment(f.ctx, w.manager, id, to, "Revive", ""); err == nil {
			t.Fatalf("disposed equipment moved to %s", to)
		}
	}
	if e, _ := f.svc.GetEquipment(f.ctx, w.eng, id); len(e.History) != 16 || e.SerialNumber != in.SerialNumber {
		t.Fatalf("a disposed machine's record: %d events", len(e.History))
	}
}

func TestFleet_ReservationsNeverDoubleBook(t *testing.T) {
	w := newFleetWorld(t)
	f := w.f
	eq := w.machine(t, "S-100")

	req := EquipmentReservationInput{EquipmentID: eq, ProjectID: f.project, Purpose: "Trench excavation", StartsOn: dayOff(5), EndsOn: dayOff(15)}
	if _, err := f.svc.CreateEquipmentReservation(f.ctx, w.officer, req); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a reservation without the permission: %v", err)
	}
	if _, err := f.svc.CreateEquipmentReservation(f.ctx, w.otherEng, req); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a reservation for someone else's project: %v", err)
	}
	backwards := req
	backwards.EndsOn = dayOff(1)
	if _, err := f.svc.CreateEquipmentReservation(f.ctx, w.eng, backwards); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a reservation that ends before it starts: %v", err)
	}
	first, err := f.svc.CreateEquipmentReservation(f.ctx, w.eng, req)
	must(t, "request", err)
	must(t, "submit", f.svc.SubmitEquipmentReservation(f.ctx, w.eng, first))
	engAsApprover := w.eng
	engAsApprover.Roles = []string{"site_engineer", "plant_manager"}
	engAsApprover.MFA, engAsApprover.AuthTime = true, time.Now()
	if err := f.svc.DecideEquipmentReservation(f.ctx, engAsApprover, first, true, ""); err == nil {
		t.Fatal("the requester approved their own reservation")
	}
	rawFails(t, f, "approve a reservation by SQL", `UPDATE equipment_reservations SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '`+first+`'`)
	must(t, "approve", f.svc.DecideEquipmentReservation(f.ctx, w.manager, first, true, ""))

	// Overlaps by one day at either end, and wholly inside, are refused; adjacent periods are not.
	for name, period := range map[string][2]int{"last day": {15, 20}, "first day": {1, 5}, "inside": {8, 9}, "around": {1, 30}} {
		id, err := f.svc.CreateEquipmentReservation(f.ctx, w.otherEng, EquipmentReservationInput{EquipmentID: eq, ProjectID: w.project2, Purpose: "Overlap " + name, StartsOn: dayOff(period[0]), EndsOn: dayOff(period[1])})
		must(t, "request "+name, err)
		must(t, "submit "+name, f.svc.SubmitEquipmentReservation(f.ctx, w.otherEng, id))
		if err := f.svc.DecideEquipmentReservation(f.ctx, w.manager, id, true, ""); !errors.Is(err, ErrDoubleBooked) {
			t.Fatalf("an overlapping reservation (%s) was approved: %v", name, err)
		}
		must(t, "reject "+name, f.svc.DecideEquipmentReservation(f.ctx, w.manager, id, false, "Machine is committed to P-001"))
	}
	w.reserve(t, eq, w.project2, w.otherEng, 16, 25)
	cal, err := f.svc.EquipmentCalendar(f.ctx, w.eng, eq, dayOff(0), dayOff(40))
	must(t, "calendar", err)
	if len(cal) != 2 || cal[0].ProjectID != f.project || cal[1].ProjectID != w.project2 {
		t.Fatalf("calendar: %+v", cal)
	}

	// Sixteen overlapping requests approved at the same moment: exactly one holds.
	eq2 := w.machine(t, "S-200")
	const n = 16
	ids := make([]string, n)
	for i := range ids {
		by, project := w.eng, f.project
		if i%2 == 1 {
			by, project = w.otherEng, w.project2
		}
		ids[i], err = f.svc.CreateEquipmentReservation(f.ctx, by, EquipmentReservationInput{EquipmentID: eq2, ProjectID: project, Purpose: fmt.Sprintf("Concurrent request %d", i), StartsOn: dayOff(10 + i%3), EndsOn: dayOff(20 + i%4)})
		must(t, "request", err)
		must(t, "submit", f.svc.SubmitEquipmentReservation(f.ctx, by, ids[i]))
	}
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := w.manager
			p.UserID = fmt.Sprintf("u-plant-manager-%d", i)
			errs[i] = f.svc.DecideEquipmentReservation(f.ctx, p, ids[i], true, "")
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		} else if !errors.Is(err, ErrDoubleBooked) {
			t.Logf("refused: %v", err)
		}
	}
	var approved int
	must(t, "count", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM equipment_reservations WHERE equipment_id = $1::uuid AND status = 'APPROVED'`, eq2).Scan(&approved)
	}))
	if won != 1 || approved != 1 {
		t.Fatalf("%d approvals succeeded and %d reservations are approved, want exactly 1", won, approved)
	}
	// The second project's reservation does not let the first project's people take the machine.
	if w.status(t, eq2) != "RESERVED" {
		t.Fatalf("status %s", w.status(t, eq2))
	}
}

func TestFleet_DailyLogsAndMeter(t *testing.T) {
	w := newFleetWorld(t)
	f := w.f
	eq := w.machine(t, "L-100")
	w.reserve(t, eq, f.project, w.eng, 0, 30)
	for _, step := range [][2]string{{"MOBILIZING", f.project}, {"ASSIGNED", ""}, {"OPERATING", ""}} {
		by := w.eng
		if step[0] == "MOBILIZING" {
			by = w.officer
		}
		if _, err := f.svc.MoveEquipment(f.ctx, by, eq, step[0], "Start of work", step[1]); err != nil {
			t.Fatalf("%s: %v", step[0], err)
		}
	}
	log := EquipmentLogInput{EquipmentID: eq, ProjectID: f.project, LogDate: dayOff(-2), Operator: "op-abebe", MeterOpen: dec("1000"), MeterClose: dec("1008"),
		ProductiveHours: dec("6.5"), IdleHours: dec("1.5"), DownHours: dec("1"), FuelLitres: dec("96"), CostCodeID: w.costCode, WorkQuantity: ptr(dec("420")), WorkUOM: "m3", Location: "Km 12+300"}

	// The operator must be authorised for the class on the day.
	if _, err := f.svc.CreateEquipmentLog(f.ctx, w.eng, log); err == nil {
		t.Fatal("a log naming an unauthorised operator was accepted")
	}
	if _, err := f.svc.CreateOperatorAuth(f.ctx, w.officer, OperatorAuthInput{Operator: w.officer.UserID, EquipmentClass: "EXCAVATOR", CertificateRef: "X-1", ValidFrom: dayOff(-1), ValidUntil: dayOff(1)}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("someone prepared their own operator authorisation: %v", err)
	}
	w.authorise(t, "op-abebe")

	// Arithmetic and place.
	for name, mut := range map[string]func(*EquipmentLogInput){
		"meter running backwards":          func(l *EquipmentLogInput) { l.MeterClose = dec("999") },
		"negative hours":                   func(l *EquipmentLogInput) { l.IdleHours = dec("-1") },
		"more than 24 hours":               func(l *EquipmentLogInput) { l.DownHours = dec("17") },
		"more engine hours than the meter": func(l *EquipmentLogInput) { l.ProductiveHours = dec("9") },
		"a future date":                    func(l *EquipmentLogInput) { l.LogDate = dayOff(1) },
		"quantity without a unit":          func(l *EquipmentLogInput) { l.WorkUOM = "" },
		"a project the machine is not at":  func(l *EquipmentLogInput) { l.ProjectID = w.project2 },
		"a meter that advances 30 in a shift": func(l *EquipmentLogInput) {
			l.MeterClose, l.ProductiveHours, l.IdleHours = dec("1030"), dec("6"), dec("1")
		},
	} {
		l := log
		mut(&l)
		by := w.eng
		if name == "a project the machine is not at" {
			by = w.otherEng
		}
		if _, err := f.svc.CreateEquipmentLog(f.ctx, by, l); err == nil {
			t.Fatalf("a log with %s was accepted", name)
		}
	}
	if _, err := f.svc.CreateEquipmentLog(f.ctx, w.pm, log); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a log by someone without the permission: %v", err)
	}

	// Offline: the same reference is recorded once; the same reference with other figures is refused.
	log.ClientRef = "tablet9-0000000042"
	first, err := f.svc.CreateEquipmentLog(f.ctx, w.eng, log)
	must(t, "log", err)
	var wg sync.WaitGroup
	repeats := make([]string, 8)
	for i := range repeats {
		wg.Add(1)
		go func(i int) { defer wg.Done(); repeats[i], _ = f.svc.CreateEquipmentLog(f.ctx, w.eng, log) }(i)
	}
	wg.Wait()
	for _, id := range repeats {
		if id != first {
			t.Fatalf("an offline repeat produced %q, want the first log %s", id, first)
		}
	}
	changed := log
	changed.ProductiveHours = dec("7")
	if _, err := f.svc.CreateEquipmentLog(f.ctx, w.eng, changed); !errors.Is(err, postgres.ErrIdempotencyConflict) {
		t.Fatalf("the same offline reference with other figures: %v", err)
	}
	// Without a reference, the day and shift are still unique.
	again := log
	again.ClientRef = ""
	if _, err := f.svc.CreateEquipmentLog(f.ctx, w.eng, again); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a second log for the same machine, day and shift: %v", err)
	}
	var count int
	must(t, "one log", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM equipment_logs WHERE equipment_id = $1::uuid`, eq).Scan(&count)
	}))
	if count != 1 {
		t.Fatalf("%d logs recorded, want 1", count)
	}

	// Approval: a second person; approval advances the meter; a decided log never changes.
	must(t, "submit", f.svc.SubmitEquipmentLog(f.ctx, w.eng, first))
	engAsPM := w.eng
	engAsPM.Roles = []string{"site_engineer", "project_manager"}
	if err := f.svc.DecideEquipmentLog(f.ctx, engAsPM, first, true, ""); err == nil {
		t.Fatal("the author approved their own log")
	}
	if err := f.svc.DecideEquipmentLog(f.ctx, w.otherEng, first, true, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another project's manager approved the log: %v", err)
	}
	rawFails(t, f, "approve a log by SQL", `UPDATE equipment_logs SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '`+first+`'`)
	rawFails(t, f, "move the meter by SQL", `UPDATE equipment SET last_meter = 5000 WHERE id = '`+eq+`'`)
	must(t, "approve", f.svc.DecideEquipmentLog(f.ctx, w.pm, first, true, ""))
	e, err := f.svc.GetEquipment(f.ctx, w.eng, eq)
	must(t, "get", err)
	if !e.LastMeter.Equal(dec("1008")) {
		t.Fatalf("meter after approval: %s", e.LastMeter)
	}
	rawFails(t, f, "edit an approved log", `UPDATE equipment_logs SET status = 'REJECTED', decision_reason = 'changed mind' WHERE id = '`+first+`'`)

	// The next log continues from the meter: a lower opening reading is refused at approval, a gap is recorded.
	next := log
	next.ClientRef, next.LogDate, next.MeterOpen, next.MeterClose = "", dayOff(-1), dec("1004"), dec("1012")
	back, err := f.svc.CreateEquipmentLog(f.ctx, w.eng, next)
	must(t, "log with a regressing meter", err)
	must(t, "submit", f.svc.SubmitEquipmentLog(f.ctx, w.eng, back))
	if err := f.svc.DecideEquipmentLog(f.ctx, w.pm, back, true, ""); err == nil {
		t.Fatal("a log whose meter starts below the last approved reading was approved")
	}
	must(t, "reject", f.svc.DecideEquipmentLog(f.ctx, w.pm, back, false, "Opening reading is below yesterday's close"))
	next.MeterOpen, next.MeterClose, next.DownHours = dec("1010"), dec("1018"), dec("0")
	gap, err := f.svc.CreateEquipmentLog(f.ctx, w.eng, next) // the rejected log freed the day
	must(t, "log after a gap", err)
	must(t, "submit", f.svc.SubmitEquipmentLog(f.ctx, w.eng, gap))
	must(t, "approve", f.svc.DecideEquipmentLog(f.ctx, w.pm, gap, true, ""))

	// Meter replacement: two people; the next log starts from the new meter.
	if _, err := f.svc.CreateMeterReset(f.ctx, w.eng, MeterResetInput{EquipmentID: eq, Kind: "REPLACEMENT", NewReading: dec("0"), Reason: "Hour meter replaced after failure"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a meter reset without the permission: %v", err)
	}
	reset, err := f.svc.CreateMeterReset(f.ctx, w.officer, MeterResetInput{EquipmentID: eq, Kind: "REPLACEMENT", NewReading: dec("0"), Reason: "Hour meter replaced after failure, job card 771"})
	must(t, "meter reset", err)
	must(t, "submit reset", f.svc.SubmitMeterReset(f.ctx, w.officer, reset))
	stale := next
	stale.LogDate, stale.MeterOpen, stale.MeterClose = dayOff(0), dec("1018"), dec("1026")
	old, err := f.svc.CreateEquipmentLog(f.ctx, w.eng, stale)
	must(t, "log on the old meter", err)
	must(t, "submit", f.svc.SubmitEquipmentLog(f.ctx, w.eng, old))
	rawFails(t, f, "approve a meter reset by SQL", `UPDATE equipment_meter_resets SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '`+reset+`'`)
	must(t, "approve reset", f.svc.DecideMeterReset(f.ctx, w.inspector, reset, true, ""))
	if e, _ = f.svc.GetEquipment(f.ctx, w.eng, eq); !e.LastMeter.IsZero() || e.MeterSerial != 2 {
		t.Fatalf("after the reset: meter %s serial %d", e.LastMeter, e.MeterSerial)
	}
	if err := f.svc.DecideEquipmentLog(f.ctx, w.pm, old, true, ""); err == nil {
		t.Fatal("a log written against the replaced meter was approved")
	}
	g, err := f.svc.GetP2PDoc(f.ctx, w.officer, "equipment_meter_reset", reset)
	must(t, "reset as a document", err)
	if g.Status != "APPROVED" {
		t.Fatalf("reset: %+v", g)
	}

	// Figures: exactly the approved logs. 6.5+6.5 productive, 1.5+1.5 idle, 1+0 down, 192 l, gap 2.
	sum, err := f.svc.FleetSummaryOf(f.ctx, w.pm, dayOff(-7), dayOff(0), "")
	must(t, "summary", err)
	if len(sum.Usage) != 1 {
		t.Fatalf("usage rows: %+v", sum.Usage)
	}
	u := sum.Usage[0]
	if u.Logs != 2 || !u.ProductiveHours.Equal(dec("13")) || !u.IdleHours.Equal(dec("3")) || !u.DownHours.Equal(dec("1")) || !u.FuelLitres.Equal(dec("192")) ||
		!u.UnloggedMeter.Equal(dec("2")) || u.Utilization == nil || !u.Utilization.Equal(dec("0.7647")) || u.Availability == nil || !u.Availability.Equal(dec("0.9412")) ||
		u.FuelPerHour == nil || !u.FuelPerHour.Equal(dec("12")) {
		t.Fatalf("usage: %+v (utilization %v availability %v fuel/h %v)", u, u.Utilization, u.Availability, u.FuelPerHour)
	}
	if sum.ByStatus["OPERATING"] != 1 || sum.Total != 1 {
		t.Fatalf("by status: %v", sum.ByStatus)
	}
	// Another project's people see the machine in the register but none of this project's hours.
	other, err := f.svc.FleetSummaryOf(f.ctx, w.otherEng, dayOff(-7), dayOff(0), "")
	must(t, "summary of another project", err)
	if len(other.Usage) != 0 || other.Total != 1 {
		t.Fatalf("another project sees usage %+v", other.Usage)
	}
	if _, err := f.svc.FleetSummaryOf(f.ctx, w.pm, dayOff(-800), dayOff(0), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an unbounded period: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestFleet_TenantIsolationAndPaging(t *testing.T) {
	w := newFleetWorld(t)
	f := w.f
	var ids []string
	for i := 0; i < 7; i++ {
		ids = append(ids, w.machine(t, fmt.Sprintf("P-%03d", i)))
	}
	// Keyset paging: every machine once, in order, three at a time.
	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		page, err := f.svc.ListEquipment(f.ctx, w.eng, EquipmentFilter{Limit: 3, Cursor: cursor})
		must(t, "page", err)
		for _, e := range page.Items {
			seen = append(seen, e.ID)
		}
		if page.Next == "" {
			if pages != 2 {
				t.Fatalf("7 machines at 3 a page took %d pages", pages+1)
			}
			break
		}
		cursor = page.Next
	}
	if fmt.Sprint(seen) != fmt.Sprint(ids) {
		t.Fatalf("paged register:\n%v\nwant\n%v", seen, ids)
	}
	if _, err := f.svc.ListEquipment(f.ctx, w.eng, EquipmentFilter{Cursor: "%%%"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a malformed cursor: %v", err)
	}
	if page, err := f.svc.ListEquipment(f.ctx, w.eng, EquipmentFilter{Search: "P-004"}); err != nil || len(page.Items) != 1 {
		t.Fatalf("search: %+v %v", page, err)
	}
	// Another tenant: nothing by any route.
	if page, err := f.svc.ListEquipment(f.ctx, f.outsider, EquipmentFilter{}); err != nil || len(page.Items) != 0 {
		t.Fatalf("another tenant lists %d machines: %v", len(page.Items), err)
	}
	if _, err := f.svc.GetEquipment(f.ctx, f.outsider, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another tenant read a machine: %v", err)
	}
	if sum, err := f.svc.FleetSummaryOf(f.ctx, f.outsider, dayOff(-7), dayOff(0), ""); err != nil || sum.Total != 0 {
		t.Fatalf("another tenant's summary: %+v %v", sum, err)
	}
	if _, err := f.svc.EquipmentCalendar(f.ctx, f.outsider, ids[0], dayOff(0), dayOff(10)); err != nil {
		t.Fatalf("calendar as another tenant: %v", err)
	}
	var rows int
	must(t, "raw read as the other tenant", f.app.WithTenant(f.ctx, f.tenant2, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM equipment) + (SELECT count(*) FROM equipment_events) + (SELECT count(*) FROM equipment_reservations)
			+ (SELECT count(*) FROM equipment_logs) + (SELECT count(*) FROM equipment_operator_auths) + (SELECT count(*) FROM equipment_meter_resets)`).Scan(&rows)
	}))
	if rows != 0 {
		t.Fatalf("the other tenant's login sees %d fleet rows", rows)
	}
	err := f.app.WithTenant(f.ctx, f.tenant2, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO equipment_reservations (equipment_id, project_id, number, purpose, starts_on, ends_on, created_by) VALUES ($1::uuid, $2::uuid, 'X-1', 'cross-tenant', current_date, current_date, 'u-x')`, ids[0], f.project)
		return err
	})
	if err == nil {
		t.Fatal("another tenant reserved this tenant's machine")
	}
}
