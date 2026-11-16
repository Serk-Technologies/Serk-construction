package kernel

import (
	"context"

	"github.com/jackc/pgx/v5"

	"errors"
	"testing"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// TestRisks_RegisterAgainstContingency (PLB-04.1, 0080). Hand-computed: the
// approved budget holds 200,000 of contingency. Risk A: 0.5 x 300,000 =
// 150,000; risk B: 0.25 x 80,000 = 20,000 -> open exposure 170,000, cover
// 30,000. A's reassessment at 0.8 x 300,000 = 240,000 makes the open exposure
// 260,000: a shortfall of 60,000. Closing B leaves 240,000 (shortfall 40,000).
// The earlier assessment of A is kept.
func TestRisks_RegisterAgainstContingency(t *testing.T) {
	b := newBudgetWorld(t)
	f, c := b.f, b.cast
	b.approvedOriginal(t)
	in := RiskInput{ProjectID: f.project, Title: "Ground water in the basement", Category: "cost", Cause: "High water table in the rainy season",
		Effect: "Dewatering and delay", Owner: "Site engineer", Response: "Programme the basement for the dry season; price dewatering",
		RiskAssessment: RiskAssessment{Probability: dec("0.5"), CostImpact: dec("300000"), ScheduleDays: 20}}
	if _, err := f.svc.RaiseRisk(f.ctx, c.buyer, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a buyer raised a project risk: %v", err)
	}
	bad := in
	bad.Probability = dec("1.2")
	if _, err := f.svc.RaiseRisk(f.ctx, c.pm, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a probability above 1 was accepted: %v", err)
	}
	ra, err := f.svc.RaiseRisk(f.ctx, c.pm, in)
	must(t, "risk A", err)
	inB := in
	inB.Title, inB.Category, inB.RiskAssessment = "Late steel delivery", "SCHEDULE", RiskAssessment{Probability: dec("0.25"), CostImpact: dec("80000"), ScheduleDays: 10}
	rb, err := f.svc.RaiseRisk(f.ctx, c.pm, inB)
	must(t, "risk B", err)

	reg, err := f.svc.ProjectRiskRegister(f.ctx, c.pm, f.project)
	must(t, "register", err)
	check := func(exposure, cover string, short bool) {
		t.Helper()
		if reg.ContingencyRemaining.StringFixed(2) != "200000.00" || reg.OpenExposure.StringFixed(2) != exposure || reg.Cover.StringFixed(2) != cover || reg.Shortfall != short {
			t.Fatalf("register: contingency %s exposure %s cover %s shortfall %v; want 200000.00 %s %s %v", reg.ContingencyRemaining, reg.OpenExposure, reg.Cover, reg.Shortfall, exposure, cover, short)
		}
	}
	check("170000.00", "30000.00", false)
	must(t, "reassess A", f.svc.AssessRisk(f.ctx, c.pm, ra, RiskAssessment{Probability: dec("0.8"), CostImpact: dec("300000"), ScheduleDays: 30, Note: "Wet season started early"}))
	reg, err = f.svc.ProjectRiskRegister(f.ctx, c.pm, f.project)
	must(t, "register", err)
	check("260000.00", "-60000.00", true)
	if err := f.svc.CloseRisk(f.ctx, c.pm, rb, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("a risk closed without a note: %v", err)
	}
	must(t, "close B", f.svc.CloseRisk(f.ctx, c.pm, rb, "Steel delivered to site"))
	if err := f.svc.AssessRisk(f.ctx, c.pm, rb, in.RiskAssessment); err == nil {
		t.Fatal("a closed risk was reassessed")
	}
	reg, err = f.svc.ProjectRiskRegister(f.ctx, c.pm, f.project)
	must(t, "register", err)
	check("240000.00", "-40000.00", true)
	var n int
	must(t, "history", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM risk_assessments WHERE risk_id = $1::uuid`, ra).Scan(&n)
	}))
	if n != 2 {
		t.Fatalf("risk A has %d assessments, want 2 (the earlier one kept)", n)
	}
	rawRefusedWith(t, f, "rewrite an assessment", "permission denied", `UPDATE risk_assessments SET probability = 0.1 WHERE risk_id = '`+ra+`'`)
	rawRefusedWith(t, f, "reopen a closed risk", "a closed risk never changes", `UPDATE project_risks SET owner = 'someone else' WHERE id = '`+rb+`'`)
	elsewhere := auth.Principal{UserID: "u-eng-elsewhere", TenantID: f.tenant, Roles: []string{"site_engineer"}, ProjectIDs: []string{"00000000-0000-0000-0000-000000000001"}, Kind: auth.KindHuman}
	if _, err := f.svc.ProjectRiskRegister(f.ctx, elsewhere, f.project); err == nil {
		t.Fatal("someone outside the project read its risk register")
	}
}
