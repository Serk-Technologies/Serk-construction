package kernel

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// TestRFIImpactAndSubmittalCycle (0090): a change event raised from an answered
// RFI names it, so the RFI's cost and time impact is computed from its change
// events; a submittal reviewed C is answered by a resubmission that names it,
// forming a cycle the database numbers and keeps a chain.
func TestRFIImpactAndSubmittalCycle(t *testing.T) {
	w := newDocWorld(t)
	f, c := w.f, w.cast
	due := time.Now().AddDate(0, 0, 7).Format("2006-01-02")

	// CNX-03.2: RFI -> change events.
	rfi, err := f.svc.CreateRFI(f.ctx, w.eng, RFIInput{ProjectID: f.project, Subject: "Slab thickness at grid C", Question: "Drawing says 200 mm, schedule says 250 mm: which governs?",
		AssignedTo: w.planner.UserID, DueOn: due})
	must(t, "rfi", err)
	ev := ChangeEventInput{ProjectID: f.project, Title: "Thicker slab at grid C", Source: "RFI", RFIID: rfi, Description: "250 mm governs per the RFI answer",
		RevenueImpct: dec("30000"), ScheduleDays: 2, Lines: []ChangeEventLine{{CostCodeID: w.costCode, CostImpact: dec("18000")}}}
	if _, err := f.svc.CreateChangeEvent(f.ctx, c.pm, ev); err == nil || !strings.Contains(err.Error(), "answered RFI") {
		t.Fatalf("a change event was raised from an unanswered RFI: %v", err)
	}
	must(t, "answer", f.svc.AnswerRFI(f.ctx, w.planner, rfi, "The schedule governs: 250 mm."))
	wrong := ev
	wrong.Source = "CLIENT"
	if _, err := f.svc.CreateChangeEvent(f.ctx, c.pm, wrong); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a change event from an RFI with another source: %v", err)
	}
	e1, err := f.svc.CreateChangeEvent(f.ctx, c.pm, ev)
	must(t, "change event 1", err)
	ev2 := ev
	ev2.Title, ev2.RevenueImpct, ev2.ScheduleDays, ev2.Lines = "Extra reinforcement at grid C", dec("0"), 1, []ChangeEventLine{{CostCodeID: w.costCode, CostImpact: dec("4500")}}
	_, err = f.svc.CreateChangeEvent(f.ctx, c.pm, ev2)
	must(t, "change event 2", err)
	rawRefusedWith(t, f, "move a change event to another RFI", "permission denied",
		`UPDATE change_events SET rfi_id = NULL WHERE id = $1::uuid`, e1)
	rawRefusedWith(t, f, "link an RFI under another source", "source RFI",
		`INSERT INTO change_events (project_id, number, title, source, description, created_by, rfi_id) VALUES ($1::uuid, 'CE-X1', 'Forged link', 'CLIENT', 'not from the RFI', 'u-pm', $2::uuid)`, f.project, rfi)
	imp, err := f.svc.RFIImpact(f.ctx, w.eng, rfi)
	must(t, "impact", err)
	if len(imp["change_events"].([]map[string]any)) != 2 || !imp["cost_impact"].(decimal.Decimal).Equal(dec("22500")) || imp["schedule_impact_days"].(int) != 3 {
		t.Fatalf("RFI impact: %v", imp)
	}
	got, err := f.svc.GetChangeEvent(f.ctx, c.pm, e1)
	must(t, "event", err)
	if got.RFIID != rfi {
		t.Fatalf("the change event does not name its RFI: %+v", got)
	}
	if _, err := f.svc.RFIImpact(f.ctx, w.outsideEng, rfi); err == nil {
		t.Fatal("someone outside the project read the RFI's impact")
	}

	// CNX-04.1.b: submittal review cycle.
	s1, err := f.svc.SubmitSubmittal(f.ctx, w.eng, SubmittalInput{ProjectID: f.project, SpecSection: "03 30 00", Title: "Concrete mix design C30"})
	must(t, "submittal", err)
	if _, err := f.svc.SubmitSubmittal(f.ctx, w.eng, SubmittalInput{ProjectID: f.project, SpecSection: "03 30 00", Title: "Too early", ResubmissionOf: s1}); err == nil {
		t.Fatal("a submittal not yet reviewed was resubmitted")
	}
	must(t, "review C", f.svc.ReviewSubmittal(f.ctx, w.pm, s1, "C", "Increase the cement content to meet the 28-day strength"))
	if _, err := f.svc.SubmitSubmittal(f.ctx, w.eng, SubmittalInput{ProjectID: f.project, SpecSection: "05 12 00", Title: "Other section", ResubmissionOf: s1}); err == nil {
		t.Fatal("a resubmission under another specification section was accepted")
	}
	s2, err := f.svc.SubmitSubmittal(f.ctx, w.eng, SubmittalInput{ProjectID: f.project, SpecSection: "03 30 00", Title: "Concrete mix design C30, revision 1", ResubmissionOf: s1})
	must(t, "resubmission", err)
	if _, err := f.svc.SubmitSubmittal(f.ctx, w.eng, SubmittalInput{ProjectID: f.project, SpecSection: "03 30 00", Title: "A second answer", ResubmissionOf: s1}); err == nil {
		t.Fatal("a submittal was resubmitted twice")
	}
	must(t, "review A", f.svc.ReviewSubmittal(f.ctx, w.pm, s2, "A", ""))
	if _, err := f.svc.SubmitSubmittal(f.ctx, w.eng, SubmittalInput{ProjectID: f.project, SpecSection: "03 30 00", Title: "After approval", ResubmissionOf: s2}); err == nil {
		t.Fatal("an approved submittal was resubmitted")
	}
	rawRefusedWith(t, f, "move a submittal in its cycle", "permission denied", `UPDATE submittals SET cycle = 5 WHERE id = $1::uuid`, s2)
	cyc, err := f.svc.SubmittalCycle(f.ctx, w.pm, s2)
	must(t, "cycle", err)
	if len(cyc) != 2 || cyc[0]["id"] != s1 || cyc[1]["id"] != s2 || fmt.Sprint(cyc[1]["cycle"]) != "2" || cyc[0]["review_code"] != "C" || cyc[1]["review_code"] != "A" {
		t.Fatalf("submittal cycle: %v", cyc)
	}
}
