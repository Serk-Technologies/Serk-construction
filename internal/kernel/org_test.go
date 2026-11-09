package kernel

// Tests for the project master, stage gates, regions, business units and the
// company calendar (WP-1.7, migration 0042, kernel/org.go). What is proven:
//  1. a project created by a person starts PLANNED in phase BID and its
//     creation is attested; raw SQL cannot create an ACTIVE project or move a
//     phase;
//  2. the phase moves only through a stage gate (prepared by one person,
//     decided by another with fresh MFA), in order, one open gate at a time,
//     and approving moves the project atomically;
//  3. master data rules (dates, coordinates, ownership) and scope;
//  4. dashboard roll-ups by region and business unit; the company calendar.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func rawFails(t *testing.T, f *fixture, name, q string) {
	t.Helper()
	err := f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, q)
		if err == nil && ct.RowsAffected() == 0 {
			return errors.New("no rows")
		}
		return err
	})
	if err == nil {
		t.Fatalf("database accepted: %s", name)
	}
	t.Logf("held: %s: %v", name, err)
}

func TestOrg_ProjectLifecycleThroughGates(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast

	// The operator's project is unchanged: ACTIVE, phase EXECUTION.
	all, err := f.svc.ListProjects(f.ctx, c.cfo)
	must(t, "list", err)
	if len(all) != 1 || all[0].Phase != "EXECUTION" || all[0].Status != "ACTIVE" {
		t.Fatalf("existing project: %+v", all)
	}

	// Creating needs project.create; the result is PLANNED in BID.
	in := ProjectInput{EntityID: f.entity, Code: "P-NEW", Name: "Hawassa Industrial Park Roads", ContractType: "REMEASURE",
		StartDate: "2027-02-01", PlannedFinish: "2028-06-30", ProjectManager: "u-pm-new"}
	if _, err := f.svc.CreateProject(f.ctx, c.pm, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager created a project: %v", err)
	}
	id, err := f.svc.CreateProject(f.ctx, c.commercial, in)
	must(t, "create project", err)
	if _, err := f.svc.CreateProject(f.ctx, c.commercial, in); err == nil {
		t.Fatal("a duplicate project code was accepted")
	}
	// The creator sees the project they created even though it is not in their scope yet.
	mine, err := f.svc.ListProjects(f.ctx, c.commercial)
	must(t, "creator list", err)
	seen := false
	for _, x := range mine {
		if x.ID == id {
			seen = true
		}
	}
	if !seen {
		t.Fatal("the creator cannot see the project they created")
	}
	proj := func() Project {
		l, err := f.svc.ListProjects(f.ctx, c.cfo)
		must(t, "list", err)
		for _, x := range l {
			if x.ID == id {
				return x
			}
		}
		t.Fatal("project missing")
		return Project{}
	}
	if p := proj(); p.Phase != "BID" || p.Status != "PLANNED" || p.CreatedBy != c.commercial.UserID {
		t.Fatalf("new project: %+v", p)
	}
	// A planned project takes no part in cost forms (active projects only).
	ref, err := f.svc.P2PReference(f.ctx, c.cfo)
	must(t, "reference", err)
	for _, r := range ref.Projects {
		if r.ID == id {
			t.Fatal("a PLANNED project appears in the active project list of forms")
		}
	}

	// Raw SQL cannot create an active project, skip the attestation or move the phase.
	rawFails(t, f, "insert an ACTIVE project", `INSERT INTO projects (legal_entity_id, code, name, status, phase, created_by) VALUES ('`+f.entity+`', 'FORGED', 'Forged project', 'ACTIVE', 'EXECUTION', 'x')`)
	rawFails(t, f, "insert a project without an attestation", `INSERT INTO projects (legal_entity_id, code, name, status, phase, created_by) VALUES ('`+f.entity+`', 'FORGED2', 'Forged project', 'PLANNED', 'BID', 'x')`)
	rawFails(t, f, "move the phase by hand", `UPDATE projects SET phase = 'EXECUTION' WHERE id = '`+id+`'`)
	rawFails(t, f, "change a project code", `UPDATE projects SET code = 'RENAMED' WHERE id = '`+id+`'`)

	// The project manager of the new project (scope follows the person's token).
	pm := auth.Principal{Kind: auth.KindHuman, UserID: "u-pm-new", TenantID: f.tenant, Roles: []string{"project_manager"}, ProjectIDs: []string{id}}
	if _, err := f.svc.CreateStageGate(f.ctx, c.pm, id, "MOBILISATION", "Contract signed and bond lodged"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a manager outside the project's scope prepared a gate: %v", err)
	}
	// Order is enforced: no skipping phases.
	if _, err := f.svc.CreateStageGate(f.ctx, pm, id, "EXECUTION", "Skipping mobilisation"); err == nil {
		t.Fatal("a gate skipped a phase")
	}
	g1, err := f.svc.CreateStageGate(f.ctx, pm, id, "MOBILISATION", "Contract signed, performance bond lodged, site handed over")
	must(t, "gate 1", err)
	if _, err := f.svc.CreateStageGate(f.ctx, pm, id, "CLOSED", "A second gate while one is open"); err == nil {
		t.Fatal("two open gates on one project")
	}
	must(t, "submit gate 1", f.svc.SubmitStageGate(f.ctx, pm, g1))
	// Maker/checker and fresh MFA: the author cannot decide, and a stale sign-in is refused.
	if err := f.svc.DecideStageGate(f.ctx, pm, g1, true, ""); err == nil {
		t.Fatal("the author decided their own gate")
	}
	stale := c.cfo
	stale.AuthTime = time.Now().Add(-time.Hour)
	if err := f.svc.DecideStageGate(f.ctx, stale, g1, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("gate approval without fresh MFA: %v", err)
	}
	rawFails(t, f, "approve a gate without an attestation", `UPDATE project_stage_gates SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '`+g1+`'`)
	if p := proj(); p.Phase != "BID" {
		t.Fatalf("the phase moved before approval: %s", p.Phase)
	}
	must(t, "approve gate 1", f.svc.DecideStageGate(f.ctx, c.cfo, g1, true, ""))
	if p := proj(); p.Phase != "MOBILISATION" || p.Status != "ACTIVE" {
		t.Fatalf("after approval: phase %s status %s", p.Phase, p.Status)
	}
	// A decided gate is immutable; the next gate starts from the new phase.
	rawFails(t, f, "edit a decided gate", `UPDATE project_stage_gates SET notes = 'edited after approval' WHERE id = '`+g1+`'`)
	// Walk the rest of the life of the project; a rejected gate leaves the phase alone.
	reject, err := f.svc.CreateStageGate(f.ctx, pm, id, "EXECUTION", "Mobilised: camp, plant and staff on site")
	must(t, "gate 2", err)
	must(t, "submit gate 2", f.svc.SubmitStageGate(f.ctx, pm, reject))
	if err := f.svc.DecideStageGate(f.ctx, c.cfo, reject, false, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("rejection without a reason: %v", err)
	}
	must(t, "reject gate 2", f.svc.DecideStageGate(f.ctx, c.cfo, reject, false, "Site handover certificate missing"))
	if p := proj(); p.Phase != "MOBILISATION" {
		t.Fatalf("a rejected gate moved the project: %s", p.Phase)
	}
	for _, step := range []string{"EXECUTION", "PRACTICAL_COMPLETION", "DEFECTS", "CLOSED"} {
		g, err := f.svc.CreateStageGate(f.ctx, pm, id, step, "Evidence for "+step+" is attached to the project file")
		must(t, "gate "+step, err)
		must(t, "submit "+step, f.svc.SubmitStageGate(f.ctx, pm, g))
		must(t, "approve "+step, f.svc.DecideStageGate(f.ctx, c.cfo, g, true, ""))
		if p := proj(); p.Phase != step {
			t.Fatalf("expected phase %s, got %s", step, p.Phase)
		}
	}
	if p := proj(); p.Status != "CLOSED" {
		t.Fatalf("a closed project is %s", p.Status)
	}
}

func TestOrg_MasterDataRegionsAndRollups(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	w.approvedBOQ(t)

	// Regions and business units: controller manages; others read or are refused.
	if _, err := f.svc.CreateOrgItem(f.ctx, c.estimator, "region", "NORTH", "Northern region"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an estimator created a region: %v", err)
	}
	north, err := f.svc.CreateOrgItem(f.ctx, c.controller, "region", "NORTH", "Northern region")
	must(t, "region", err)
	south, err := f.svc.CreateOrgItem(f.ctx, c.controller, "region", "SOUTH", "Southern region")
	must(t, "region 2", err)
	if _, err := f.svc.CreateOrgItem(f.ctx, c.controller, "region", "NORTH", "Duplicate"); err == nil {
		t.Fatal("a duplicate region code was accepted")
	}
	roads, err := f.svc.CreateOrgItem(f.ctx, c.controller, "business_unit", "ROADS", "Roads and bridges")
	must(t, "business unit", err)
	if _, err := f.svc.CreateOrgItem(f.ctx, c.controller, "galaxy", "X", "Nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown org kind: %v", err)
	}
	if l, err := f.svc.ListOrgItems(f.ctx, c.pm, "region"); err != nil || len(l) != 2 {
		t.Fatalf("regions listed: %v %v", l, err)
	}

	// The project manager edits their project's master data.
	lat, lng := decimal.RequireFromString("13.496700"), decimal.RequireFromString("39.475300")
	upd := ProjectInput{Name: "Mekelle Ring Road (updated)", RegionID: north, BusinessUnitID: roads, ContractType: "REMEASURE", Latitude: &lat, Longitude: &lng,
		StartDate: "2026-01-01", PlannedFinish: "2027-12-31", ProjectManager: "u-pm", Description: "Ring road lot 2"}
	must(t, "update", f.svc.UpdateProject(f.ctx, c.pm, f.project, upd))
	bad := upd
	bad.PlannedFinish = "2025-01-01"
	if err := f.svc.UpdateProject(f.ctx, c.pm, f.project, bad); err == nil {
		t.Fatal("a finish date before the start date was accepted")
	}
	bad = upd
	badLat := decimal.RequireFromString("123")
	bad.Latitude = &badLat
	if err := f.svc.UpdateProject(f.ctx, c.pm, f.project, bad); err == nil {
		t.Fatal("a latitude of 123 was accepted")
	}
	bad = upd
	bad.StartDate = "tomorrow"
	if err := f.svc.UpdateProject(f.ctx, c.pm, f.project, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a malformed date: %v", err)
	}
	if err := f.svc.UpdateProject(f.ctx, c.estimator, f.project, upd); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an estimator edited a project: %v", err)
	}
	outsider := auth.Principal{Kind: auth.KindHuman, UserID: "u-pm-out", TenantID: f.tenant, Roles: []string{"project_manager"}, ProjectIDs: []string{}}
	if err := f.svc.UpdateProject(f.ctx, outsider, f.project, upd); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a manager outside the project's scope edited it: %v", err)
	}
	// A region of another company is invisible, so it cannot be attached.
	bad = upd
	bad.RegionID = "00000000-0000-4000-8000-000000000000"
	if err := f.svc.UpdateProject(f.ctx, c.pm, f.project, bad); err == nil {
		t.Fatal("an unknown region was attached")
	}
	l, err := f.svc.ListProjects(f.ctx, c.pm)
	must(t, "list", err)
	if len(l) != 1 || l[0].Region != "Northern region" || l[0].BusinessUnit != "Roads and bridges" || l[0].ProjectManager != "u-pm" || l[0].ContractType != "REMEASURE" {
		t.Fatalf("project master data: %+v", l)
	}
	if l[0].Code != "P-API" && l[0].Code == "" {
		t.Fatalf("code lost")
	}

	// Roll-ups: the dashboard narrows to a region and business unit.
	d, err := f.svc.DashboardFor(f.ctx, c.cfo, "project", DashboardFilter{RegionID: north})
	must(t, "north dashboard", err)
	if d.Projects != 1 {
		t.Fatalf("north has %d projects, want 1", d.Projects)
	}
	d, err = f.svc.DashboardFor(f.ctx, c.cfo, "project", DashboardFilter{RegionID: south})
	must(t, "south dashboard", err)
	if d.Projects != 0 {
		t.Fatalf("south has %d projects, want 0", d.Projects)
	}
	if b, _ := tileByID(d, "K-BUD-01"); b.Value != "-" {
		t.Fatalf("an empty region shows budget %q", b.Value)
	}
	d, err = f.svc.DashboardFor(f.ctx, c.cfo, "project", DashboardFilter{RegionID: north, BusinessUnitID: roads})
	must(t, "north roads dashboard", err)
	if d.Projects != 1 {
		t.Fatalf("north roads has %d projects", d.Projects)
	}

	// The company calendar.
	if _, err := f.svc.AddCalendarException(f.ctx, c.estimator, "2027-01-07", "Genna", "HOLIDAY"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an estimator edited the calendar: %v", err)
	}
	h, err := f.svc.AddCalendarException(f.ctx, c.controller, "2027-01-07", "Ethiopian Christmas (Genna)", "HOLIDAY")
	must(t, "holiday", err)
	if _, err := f.svc.AddCalendarException(f.ctx, c.controller, "2027-01-07", "Again", "HOLIDAY"); err == nil {
		t.Fatal("two entries for one day")
	}
	if _, err := f.svc.AddCalendarException(f.ctx, c.controller, "07/01/2027", "Bad", "HOLIDAY"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a malformed day: %v", err)
	}
	if _, err := f.svc.AddCalendarException(f.ctx, c.controller, "2027-02-06", "Saturday worked", "WORKING"); err != nil {
		t.Fatalf("working day: %v", err)
	}
	cal, err := f.svc.ListCalendarExceptions(f.ctx, c.pm)
	must(t, "calendar", err)
	if len(cal) != 2 || cal[0].Day != "2027-01-07" {
		t.Fatalf("calendar: %+v", cal)
	}
	must(t, "remove", f.svc.RemoveCalendarException(f.ctx, c.controller, h))
	if err := f.svc.RemoveCalendarException(f.ctx, c.controller, h); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing twice: %v", err)
	}
}
