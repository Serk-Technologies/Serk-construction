package kernel

// Tests for change events (WP-2.4, migration 0045, kernel/change.go). Proven:
//  1. input rules, project scope and permissions;
//  2. maker/checker, fresh MFA, human-only decisions, attestation;
//  3. nothing is editable once submitted or decided; the database refuses raw SQL;
//  4. approving drafts a budget REVISION for the cost lines (draft only), and the
//     budget moves only after that revision is itself approved by someone else;
//  5. a rejected event, or one with no cost lines, raises no budget draft.

import (
	"errors"
	"testing"
	"time"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func TestChange_EventToBudgetDraft(t *testing.T) {
	b := newBudgetWorld(t)
	f, c := b.f, b.cast
	b.approvedOriginal(t) // 10-MAT 1,000,000; 20-FIN 300,000; 90-CNT 200,000

	in := ChangeEventInput{ProjectID: f.project, Title: "Upgrade slab to C35", Source: "CLIENT", Description: "Client requests stronger concrete for level 3 slab",
		RevenueImpct: dec("80000"), ScheduleDays: 5,
		Lines: []ChangeEventLine{{CostCodeID: b.mat, CostImpact: dec("50000")}, {CostCodeID: b.fin, CostImpact: dec("-10000"), Note: "less screed"}}}
	outsider := auth.Principal{Kind: auth.KindHuman, UserID: "u-qs-out", TenantID: f.tenant, Roles: []string{"quantity_surveyor"}, ProjectIDs: []string{"00000000-0000-0000-0000-000000000001"}}
	if _, err := f.svc.CreateChangeEvent(f.ctx, outsider, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a QS outside the project raised a change: %v", err)
	}
	if _, err := f.svc.CreateChangeEvent(f.ctx, c.estimator, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an estimator raised a change: %v", err)
	}
	bad := in
	bad.Source = "WISHFUL"
	if _, err := f.svc.CreateChangeEvent(f.ctx, c.pm, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad source: %v", err)
	}
	bad = in
	bad.Lines = []ChangeEventLine{{CostCodeID: b.mat, CostImpact: dec("1")}, {CostCodeID: b.mat, CostImpact: dec("2")}}
	if _, err := f.svc.CreateChangeEvent(f.ctx, c.pm, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate cost code: %v", err)
	}
	bad = in
	bad.Lines = []ChangeEventLine{{CostCodeID: b.mat, CostImpact: dec("0")}}
	if _, err := f.svc.CreateChangeEvent(f.ctx, c.pm, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero line: %v", err)
	}

	id, err := f.svc.CreateChangeEvent(f.ctx, c.pm, in)
	must(t, "create", err)
	e, err := f.svc.GetChangeEvent(f.ctx, c.pm, id)
	must(t, "get", err)
	wantAmount(t, "cost impact", e.CostImpact.String(), "40000")
	wantAmount(t, "margin impact", e.Margin.String(), "40000") // 80,000 revenue - 40,000 cost
	if e.Status != "DRAFT" || len(e.Lines) != 2 || e.ScheduleDays != 5 {
		t.Fatalf("event: %+v", e)
	}

	// An event with no impact at all cannot be submitted.
	empty, err := f.svc.CreateChangeEvent(f.ctx, c.pm, ChangeEventInput{ProjectID: f.project, Title: "Query only", Source: "RFI", Description: "Clarification asked of the designer"})
	must(t, "empty", err)
	if err := f.svc.SubmitChangeEvent(f.ctx, c.pm, empty); err == nil {
		t.Fatal("an event with no impact was submitted")
	}

	rawFails(t, f, "add a line to a submitted event later", `INSERT INTO change_event_lines (event_id, cost_code_id, cost_impact) SELECT '`+id+`', cost_code_id, 1 FROM change_event_lines WHERE event_id = '`+id+`' LIMIT 0`)
	must(t, "submit", f.svc.SubmitChangeEvent(f.ctx, c.pm, id))
	rawFails(t, f, "change a submitted event's revenue", `UPDATE change_events SET revenue_impact = 1 WHERE id = '`+id+`'`)
	rawFails(t, f, "add a line after submission", `INSERT INTO change_event_lines (event_id, cost_code_id, cost_impact) VALUES ('`+id+`', '`+b.cnt+`', 5)`)
	rawFails(t, f, "link a budget draft out of band", `INSERT INTO change_event_budget (event_id, budget_version_id) SELECT '`+id+`', id FROM budget_versions LIMIT 1`)

	if err := f.svc.DecideChangeEvent(f.ctx, c.pm, id, true, ""); err == nil {
		t.Fatal("the author decided their own change event")
	}
	fresh := c.commercial
	fresh.MFA, fresh.AuthTime = true, time.Now()
	stale := c.commercial
	stale.MFA, stale.AuthTime = true, time.Now().Add(-time.Hour)
	if err := f.svc.DecideChangeEvent(f.ctx, stale, id, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("approval without fresh MFA: %v", err)
	}
	ai := fresh
	ai.Kind = auth.KindAI
	if err := f.svc.DecideChangeEvent(f.ctx, ai, id, true, ""); err == nil {
		t.Fatal("an AI principal decided a change event")
	}
	rawFails(t, f, "approve without an attestation", `UPDATE change_events SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '`+id+`'`)
	if err := f.svc.DecideChangeEvent(f.ctx, fresh, id, false, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("rejection without a reason: %v", err)
	}

	// Approval drafts a budget revision; the budget itself has not moved.
	must(t, "approve", f.svc.DecideChangeEvent(f.ctx, fresh, id, true, ""))
	e, err = f.svc.GetChangeEvent(f.ctx, c.pm, id)
	must(t, "get approved", err)
	if e.Status != "APPROVED" || e.BudgetDraftID == "" {
		t.Fatalf("after approval: %+v", e)
	}
	wantAmount(t, "mat budget before the revision", b.position(t)["10-MAT"].Current.String(), "1000000")
	rawFails(t, f, "edit an approved event", `UPDATE change_events SET revenue_impact = 1 WHERE id = '`+id+`'`)

	// The draft is an ordinary revision: its author submits, a different person approves.
	must(t, "submit revision", f.svc.SubmitBudgetRevision(f.ctx, c.pm, e.BudgetDraftID))
	if err := f.svc.DecideBudgetRevision(f.ctx, c.pm, e.BudgetDraftID, true, ""); err == nil {
		t.Fatal("the author approved the revision drafted from their own change")
	}
	must(t, "approve revision", f.svc.DecideBudgetRevision(f.ctx, c.commercial, e.BudgetDraftID, true, "change approved"))
	pos := b.position(t)
	wantAmount(t, "mat budget after the revision", pos["10-MAT"].Current.String(), "1050000")
	wantAmount(t, "fin budget after the revision", pos["20-FIN"].Current.String(), "290000")

	// A revenue-only event raises no budget draft; a rejected event raises none either.
	rev, err := f.svc.CreateChangeEvent(f.ctx, c.pm, ChangeEventInput{ProjectID: f.project, Title: "Time extension", Source: "SITE_INSTRUCTION", Description: "Rain delay instruction", ScheduleDays: 12})
	must(t, "revenue only", err)
	must(t, "submit 2", f.svc.SubmitChangeEvent(f.ctx, c.pm, rev))
	must(t, "approve 2", f.svc.DecideChangeEvent(f.ctx, fresh, rev, true, ""))
	if e2, _ := f.svc.GetChangeEvent(f.ctx, c.pm, rev); e2.BudgetDraftID != "" {
		t.Fatalf("an event with no cost lines raised a budget draft: %+v", e2)
	}
	rej, err := f.svc.CreateChangeEvent(f.ctx, c.pm, in)
	must(t, "rejected one", err)
	must(t, "submit 3", f.svc.SubmitChangeEvent(f.ctx, c.pm, rej))
	must(t, "reject", f.svc.DecideChangeEvent(f.ctx, fresh, rej, false, "Client withdrew the request"))
	if e3, _ := f.svc.GetChangeEvent(f.ctx, c.pm, rej); e3.Status != "REJECTED" || e3.BudgetDraftID != "" {
		t.Fatalf("rejected event: %+v", e3)
	}
}

// An approved change event is recovered through contract variations (0054).
func TestChange_LinkedVariations(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	ctr := w.activeContract(t)
	ev, err := f.svc.CreateChangeEvent(f.ctx, c.pm, ChangeEventInput{ProjectID: f.project, Title: "River protection", Source: "CLIENT",
		Description: "Gabions after the flood survey", RevenueImpct: dec("1850000"), ScheduleDays: 10})
	must(t, "event", err)
	vo := func(title string) string {
		id, err := f.svc.CreateVariation(f.ctx, c.commercial, ctr, VariationInput{Title: title, Reason: "Client instruction after the flood survey",
			ClientInstructionRef: "ERA-VO-" + title[:3], ClientInstructionSHA256: voSHA,
			Lines: []VariationLine{{LineNo: 4, Kind: "NEW", Description: "Gabion walls " + title, Unit: "m3", QtyDelta: dec("500"), Rate: dec("3700")}}})
		must(t, "variation "+title, err)
		return id
	}
	v1 := vo("Gab A")
	if err := f.svc.LinkVariation(f.ctx, c.pm, ev, v1); err == nil {
		t.Fatal("a draft change event was linked")
	}
	must(t, "submit", f.svc.SubmitChangeEvent(f.ctx, c.pm, ev))
	must(t, "approve", f.svc.DecideChangeEvent(f.ctx, w.mfa(c.commercial), ev, true, ""))
	if err := f.svc.LinkVariation(f.ctx, w.ar.arClerk, ev, v1); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an AR clerk linked a variation: %v", err)
	}
	must(t, "link", f.svc.LinkVariation(f.ctx, c.pm, ev, v1))
	if err := f.svc.LinkVariation(f.ctx, c.pm, ev, v1); err == nil {
		t.Fatal("the same variation was linked twice")
	}
	must(t, "submit vo", f.svc.SubmitVariation(f.ctx, c.commercial, v1))
	must(t, "approve vo", f.svc.DecideVariation(f.ctx, w.mfa(c.cfo), v1, true, ""))
	e, err := f.svc.GetChangeEvent(f.ctx, c.pm, ev)
	must(t, "get", err)
	if len(e.Variations) != 1 || e.Variations[0]["status"] != "APPROVED" || !e.RecoveredValue.Equal(dec("1850000")) {
		t.Fatalf("linked variations: %+v recovered %s", e.Variations, e.RecoveredValue)
	}
	rawFails(t, f, "remove a link", `DELETE FROM change_event_variations WHERE event_id = '`+ev+`'`)
}
