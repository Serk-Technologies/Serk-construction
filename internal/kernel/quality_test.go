package kernel

// Tests for quality and site records (WP-3.6, WP-3.8, migration 0065, kernel/quality.go).
// Fixture: activity B (excavation) carries a HOLD item "rebar inspection" at 0 percent; progress on B is
// refused until the inspection passes. A concrete cube test of 18 N/mm2 against a minimum of 25 fails and
// raises a non-conformance by itself.

import (
	"errors"
	"testing"
	"time"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func TestQuality_HoldPointNCRAndSiteRecords(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	w.approvedBOQ(t)
	now := time.Now()
	person := func(id, role string, projects []string) auth.Principal {
		return auth.Principal{UserID: id, TenantID: f.tenant, Roles: []string{role}, ProjectIDs: projects, IssuedAt: now.Add(-time.Minute), Kind: auth.KindHuman}
	}
	proj := []string{f.project}
	planner := person("u-planner", "planner", proj)
	qe := person("u-quality", "quality_engineer", nil)
	eng, eng2, pm := c.siteEng, person("u-site-engineer-2", "site_engineer", proj), c.pm

	// A baseline programme to progress against.
	v1, err := f.svc.CreateSchedule(f.ctx, planner, w.scheduleInput())
	must(t, "schedule", err)
	must(t, "schedule submit", f.svc.SubmitSchedule(f.ctx, planner, v1))
	must(t, "baseline", f.svc.DecideSchedule(f.ctx, pm, v1, true, ""))

	// ITP: prepared by the quality engineer, approved by the project manager.
	if _, err := f.svc.CreateITP(f.ctx, eng, ITPIn{ProjectID: f.project, Title: "Excavation", ActivityCode: "B", Items: []ITPItem{{Description: "x", Kind: "HOLD"}}}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a site engineer prepared a plan: %v", err)
	}
	itp, err := f.svc.CreateITP(f.ctx, qe, ITPIn{ProjectID: f.project, Title: "Excavation and rebar", ActivityCode: "b", Items: []ITPItem{
		{Description: "Rebar inspection before pouring", Kind: "HOLD"}, {Description: "Concrete cube sampling", Kind: "WITNESS"}}})
	must(t, "itp", err)
	if _, err := f.svc.RequestInspection(f.ctx, eng, InspectionIn{ProjectID: f.project, ITPID: itp, ItemSeq: 1, RequestedFor: today().Format("2006-01-02")}); err == nil {
		t.Fatal("an inspection was requested against an unapproved plan")
	}
	must(t, "submit itp", f.svc.SubmitITP(f.ctx, qe, itp))
	if err := f.svc.DecideITP(f.ctx, qe, itp, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the quality engineer approved their own plan: %v", err)
	}
	must(t, "approve itp", f.svc.DecideITP(f.ctx, pm, itp, true, ""))

	// The hold point stops progress on B.
	progress := func(pc string) error {
		st := d("2026-10-06")
		_, err := f.svc.RecordProgress(f.ctx, planner, f.project, d("2026-10-14"), []ProgressLine{{ActivityCode: "B", PercentComplete: dec(pc), ActualStart: &st}})
		return err
	}
	if err := progress("40"); err == nil {
		t.Fatal("progress above an unpassed hold point was accepted")
	}
	ir, err := f.svc.RequestInspection(f.ctx, eng, InspectionIn{ProjectID: f.project, ITPID: itp, ItemSeq: 1, RequestedFor: today().Format("2006-01-02"), Location: "Block A"})
	must(t, "request inspection", err)
	selfQE := qe
	selfQE.UserID = eng.UserID
	if err := f.svc.DecideInspection(f.ctx, selfQE, ir, true, ""); err == nil {
		t.Fatal("the requester decided their own inspection")
	}
	must(t, "fail inspection", f.svc.DecideInspection(f.ctx, qe, ir, false, "Spacing of the bars is out of tolerance"))
	if err := progress("40"); err == nil {
		t.Fatal("progress was allowed after a failed inspection")
	}
	ir2, err := f.svc.RequestInspection(f.ctx, eng, InspectionIn{ProjectID: f.project, ITPID: itp, ItemSeq: 1, RequestedFor: today().Format("2006-01-02")})
	must(t, "re-request", err)
	must(t, "pass inspection", f.svc.DecideInspection(f.ctx, qe, ir2, true, ""))
	must(t, "progress after the hold point", progress("40"))

	// A failed test raises a non-conformance; it is actioned and closed by different people.
	min := dec("25")
	_, passed, err := f.svc.RecordMaterialTest(f.ctx, qe, MaterialTestIn{ProjectID: f.project, BatchRef: "C25-0412", TestType: "Cube strength 28 days", Value: dec("18"), Unit: "N/mm2", SpecMin: &min, TestedOn: today().Format("2006-01-02")})
	must(t, "test", err)
	if passed {
		t.Fatal("18 N/mm2 passed a minimum of 25")
	}
	_, passed, err = f.svc.RecordMaterialTest(f.ctx, qe, MaterialTestIn{ProjectID: f.project, BatchRef: "C25-0413", TestType: "Cube strength 28 days", Value: dec("31"), Unit: "N/mm2", SpecMin: &min, TestedOn: today().Format("2006-01-02")})
	must(t, "test 2", err)
	if !passed {
		t.Fatal("31 N/mm2 failed a minimum of 25")
	}
	qv, err := f.svc.QualityView(f.ctx, qe)
	must(t, "quality view", err)
	ncrs := qv["ncrs"].([]map[string]any)
	if len(ncrs) != 1 || ncrs[0]["from_test"] != true {
		t.Fatalf("ncrs: %v", ncrs)
	}
	ncr := ncrs[0]["id"].(string)
	if err := f.svc.CloseNCR(f.ctx, pm, ncr, "Looks fine to me"); err == nil {
		t.Fatal("an unactioned non-conformance was closed")
	}
	must(t, "action", f.svc.ActionNCR(f.ctx, qe, ncr, "Poor curing in hot weather", "Cut cores from the pour and retest; cure with wet hessian", dec("45000")))
	if err := f.svc.CloseNCR(f.ctx, eng, ncr, "Verified on site"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a site engineer closed a non-conformance: %v", err)
	}
	must(t, "close ncr", f.svc.CloseNCR(f.ctx, pm, ncr, "Cores retested at 29 N/mm2; verified on site"))
	rawFails(t, f, "edit a closed non-conformance", `UPDATE ncrs SET description = 'changed' WHERE id = '`+ncr+`'`)

	// Punch items: raised, fixed, closed by someone other than the fixer.
	pi, err := f.svc.RaisePunch(f.ctx, eng, PunchIn{ProjectID: f.project, Location: "Block A level 2", Description: "Cracked tile in the corridor", Priority: "high", DueOn: today().AddDate(0, 0, 7).Format("2006-01-02")})
	must(t, "punch", err)
	must(t, "fix", f.svc.FixPunch(f.ctx, eng2, pi))
	if err := f.svc.ClosePunch(f.ctx, eng2, pi); err == nil {
		t.Fatal("the fixer closed their own punch item")
	}
	must(t, "close punch", f.svc.ClosePunch(f.ctx, eng, pi))

	// Site instruction: issued by the project manager, acknowledged by someone else.
	if _, err := f.svc.IssueInstruction(f.ctx, eng, InstructionIn{ProjectID: f.project, Title: "Change of slab level", Body: "Raise the slab by 50 mm"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a site engineer issued an instruction: %v", err)
	}
	si, err := f.svc.IssueInstruction(f.ctx, pm, InstructionIn{ProjectID: f.project, Title: "Change of slab level", Body: "Raise the slab by 50 mm on the architect's drawing A-102 rev C"})
	must(t, "instruction", err)
	if err := f.svc.AckInstruction(f.ctx, pm, si); err == nil {
		t.Fatal("the issuer acknowledged their own instruction")
	}
	must(t, "ack", f.svc.AckInstruction(f.ctx, eng, si))
	must(t, "close instruction", f.svc.CloseInstruction(f.ctx, pm, si))

	// Meeting minutes: issued minutes never change.
	mt, err := f.svc.CreateMeeting(f.ctx, eng, MeetingIn{ProjectID: f.project, Kind: "progress", Title: "Weekly progress meeting 41", HeldOn: today().Format("2006-01-02"), Attendees: "PM, QS, site engineer",
		Minutes: "Excavation 40% complete; rebar inspection passed; slab level instruction acknowledged.", Actions: []MeetingAction{{Description: "Order the blinding concrete", Owner: "Procurement", DueOn: today().AddDate(0, 0, 3).Format("2006-01-02")}}})
	must(t, "meeting", err)
	must(t, "issue minutes", f.svc.IssueMinutes(f.ctx, eng, mt))
	rawFails(t, f, "edit issued minutes", `UPDATE meetings SET minutes = 'changed' WHERE id = '`+mt+`'`)
	sv, err := f.svc.SiteView(f.ctx, eng)
	must(t, "site view", err)
	mm := sv["meetings"].([]map[string]any)
	if len(mm) != 1 {
		t.Fatalf("meetings: %v", mm)
	}
	acts := mm[0]["actions"].([]any)
	must(t, "action done", f.svc.CompleteMeetingAction(f.ctx, eng, acts[0].(map[string]any)["id"].(string)))
	if _, err := f.svc.QualityView(f.ctx, w.cast.buyer); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a buyer read the quality register: %v", err)
	}
}
