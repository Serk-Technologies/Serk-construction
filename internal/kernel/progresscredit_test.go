package kernel

// Tests for rules of credit and installed quantities (WP-3.9, migration 0067, kernel/progresscredit.go).
// Rule for activity B: formwork 20, rebar 30, pour 40, curing 10. Formwork and rebar done -> 50 percent.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func TestCredit_RulesToProgress(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	w.approvedBOQ(t)
	now := time.Now()
	planner := auth.Principal{Kind: auth.KindHuman, UserID: "u-planner", TenantID: f.tenant, Roles: []string{"planner"}, ProjectIDs: []string{f.project}, IssuedAt: now.Add(-time.Minute)}
	eng, pm := c.siteEng, c.pm
	v1, err := f.svc.CreateSchedule(f.ctx, planner, w.scheduleInput())
	must(t, "schedule", err)
	must(t, "submit", f.svc.SubmitSchedule(f.ctx, planner, v1))
	must(t, "baseline", f.svc.DecideSchedule(f.ctx, pm, v1, true, ""))

	steps := []CreditStep{{"Formwork", dec("20")}, {"Rebar", dec("30")}, {"Pour", dec("40")}, {"Curing", dec("10")}}
	if _, err := f.svc.CreateCreditRule(f.ctx, eng, CreditRuleIn{ProjectID: f.project, ActivityCode: "B", Name: "Excavation", Steps: steps}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an engineer prepared a rule: %v", err)
	}
	bad, err := f.svc.CreateCreditRule(f.ctx, planner, CreditRuleIn{ProjectID: f.project, ActivityCode: "b", Name: "Short", Steps: []CreditStep{{"One", dec("40")}, {"Two", dec("40")}}})
	must(t, "bad rule", err)
	if err := f.svc.ApproveCreditRule(f.ctx, pm, bad); err == nil {
		t.Fatal("a rule whose weights add up to 80 was approved")
	}
	rule, err := f.svc.CreateCreditRule(f.ctx, planner, CreditRuleIn{ProjectID: f.project, ActivityCode: "B", Name: "Excavation and foundation concrete", Steps: steps})
	must(t, "rule", err)
	if err := f.svc.CompleteCreditStep(f.ctx, eng, rule, 1, today().Format("2006-01-02")); err == nil {
		t.Fatal("a step was completed under an unapproved rule")
	}
	if err := f.svc.ApproveCreditRule(f.ctx, planner, rule); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the planner approved their own rule: %v", err)
	}
	must(t, "approve", f.svc.ApproveCreditRule(f.ctx, pm, rule))
	d1, d2 := today().AddDate(0, 0, -3).Format("2006-01-02"), today().AddDate(0, 0, -1).Format("2006-01-02")
	if err := f.svc.CompleteCreditStep(f.ctx, eng, rule, 2, d1); err == nil {
		t.Fatal("step 2 was completed before step 1")
	}
	must(t, "step 1", f.svc.CompleteCreditStep(f.ctx, eng, rule, 1, d1))
	must(t, "step 2", f.svc.CompleteCreditStep(f.ctx, eng, rule, 2, d2))
	if err := f.svc.CompleteCreditStep(f.ctx, eng, rule, 2, d2); err == nil {
		t.Fatal("a step was completed twice")
	}
	cv, err := f.svc.CreditView(f.ctx, planner)
	must(t, "view", err)
	var pct string
	for _, r := range cv["rules"].([]map[string]any) {
		if r["id"] == rule {
			pct = r["percent"].(interface{ String() string }).String()
		}
	}
	if !dec(pct).Equal(dec("50")) {
		t.Fatalf("percent %s, want 50", pct)
	}
	if _, err := f.svc.ProgressFromRules(f.ctx, eng, f.project, today().Format("2006-01-02")); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an engineer turned rules into progress: %v", err)
	}
	prog, err := f.svc.ProgressFromRules(f.ctx, planner, f.project, today().Format("2006-01-02"))
	must(t, "progress", err)
	var got string
	must(t, "lines", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT activity_code || '=' || percent_complete::numeric(5,2) || '/' || actual_start FROM schedule_progress_lines WHERE progress_id = $1::uuid`, prog).Scan(&got)
	}))
	if got != "B=50.00/"+d1 {
		t.Fatalf("progress line: %s", got)
	}
	must(t, "installed", f.svc.RecordInstalled(f.ctx, eng, InstalledIn{ProjectID: f.project, ActivityCode: "B", Description: "Strip foundation concrete", Quantity: dec("42.5"), Unit: "m3", InstalledOn: d2}))
	must(t, "installed 2", f.svc.RecordInstalled(f.ctx, eng, InstalledIn{ProjectID: f.project, ActivityCode: "B", Description: "Strip foundation concrete", Quantity: dec("20"), Unit: "m3", InstalledOn: today().Format("2006-01-02")}))
	rawFails(t, f, "edit an installed quantity", `UPDATE installed_quantities SET quantity = 1`)
	cv, _ = f.svc.CreditView(f.ctx, planner)
	inst := cv["installed"].([]map[string]any)
	if len(inst) != 2 || !dec(inst[0]["cumulative"].(interface{ String() string }).String()).Equal(dec("62.5")) {
		t.Fatalf("installed: %v", inst)
	}
}
