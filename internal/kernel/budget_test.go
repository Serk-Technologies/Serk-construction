package kernel

// Tests for budget control (WP-1.3, migration 0039, kernel/budget.go).
//
// What is proven here, in order:
//  1. the arithmetic: current = original + revisions + transfers in - out
//     + drawdowns in - out, per cost code;
//  2. maker/checker and human-only decisions;
//  3. every limit: one approved ORIGINAL, transfers and drawdowns within the
//     source code's available budget, drawdowns only from contingency;
//  4. the database refuses the same violations when the kernel is bypassed
//     (raw SQL as the tenant's own login), and isolates tenants in the view;
//  5. two transfers racing for the same headroom: exactly one wins.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
)

// budgetWorld is the P2P world plus an approved WBS version with three codes:
// 10-MAT (materials), 20-FIN (finishes) and 90-CNT (contingency).
type budgetWorld struct {
	*p2pWorld
	mat, fin, cnt string
}

func newBudgetWorld(t *testing.T) *budgetWorld {
	t.Helper()
	w := newP2PWorld(t)
	f := w.f
	planner := auth.Principal{Kind: auth.KindHuman, UserID: "u-wbs-planner-2", TenantID: f.tenant, Roles: []string{"project_manager"}, ProjectIDs: []string{f.project}}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v, _, err := f.svc.ProposeWBSVersion(f.ctx, planner, f.project, "Construction WBS", []postgres.WBSNode{{Code: "10", Name: "Works"}, {Code: "90", Name: "Reserves"}},
		[]postgres.CostCode{
			{NodeCode: "10", Code: "10-MAT", Name: "Materials", CostType: "MATERIAL", ValidFrom: from},
			{NodeCode: "10", Code: "20-FIN", Name: "Finishes", CostType: "MATERIAL", ValidFrom: from},
			{NodeCode: "90", Code: "90-CNT", Name: "Contingency", CostType: "PRELIMINARIES", ValidFrom: from},
		})
	must(t, "wbs v2", err)
	must(t, "wbs v2 approve", f.svc.DecideWBSVersion(f.ctx, f.appr, v.ID, true, "construction WBS approved"))
	b := &budgetWorld{p2pWorld: w}
	must(t, "codes", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for code, dst := range map[string]*string{"10-MAT": &b.mat, "20-FIN": &b.fin, "90-CNT": &b.cnt} {
			if err := tx.QueryRow(ctx, `SELECT c.id::text FROM cost_codes c JOIN wbs_nodes n ON n.id = c.wbs_node_id
				WHERE n.version_id = $1::uuid AND c.code = $2`, v.ID, code).Scan(dst); err != nil {
				return err
			}
		}
		return nil
	}))
	return b
}

// approvedOriginal creates, submits and approves the ORIGINAL budget:
// 10-MAT 1,000,000; 20-FIN 300,000; 90-CNT 200,000 (contingency).
func (b *budgetWorld) approvedOriginal(t *testing.T) string {
	t.Helper()
	f, c := b.f, b.cast
	id, err := f.svc.CreateBudgetRevision(f.ctx, c.pm, f.project, "ORIGINAL", "Construction baseline", []BudgetLine{
		{CostCodeID: b.mat, Amount: dec("1000000")}, {CostCodeID: b.fin, Amount: dec("300000")}, {CostCodeID: b.cnt, Amount: dec("200000"), ContingencyFlag: true}})
	must(t, "create original", err)
	must(t, "submit original", f.svc.SubmitBudgetRevision(f.ctx, c.pm, id))
	must(t, "approve original", f.svc.DecideBudgetRevision(f.ctx, c.commercial, id, true, "baseline approved"))
	return id
}

func (b *budgetWorld) position(t *testing.T) map[string]BudgetCurrentPosition {
	t.Helper()
	rows, err := b.f.svc.GetProjectBudgetCurrent(b.f.ctx, b.cast.pm, b.f.project)
	must(t, "budget position", err)
	out := map[string]BudgetCurrentPosition{}
	for _, r := range rows {
		out[r.CostCode] = r
	}
	return out
}

func wantAmount(t *testing.T, what string, got, want string) {
	t.Helper()
	if !dec(got).Equal(dec(want)) {
		t.Fatalf("%s: got %s, want %s", what, got, want)
	}
}

func TestBudget_ArithmeticAndControls(t *testing.T) {
	b := newBudgetWorld(t)
	f, c := b.f, b.cast
	orig := b.approvedOriginal(t)

	pos := b.position(t)
	wantAmount(t, "10-MAT original", pos["10-MAT"].Current.String(), "1000000")
	if !pos["90-CNT"].IsContingency || pos["10-MAT"].IsContingency {
		t.Fatalf("contingency flag: 90-CNT=%v 10-MAT=%v", pos["90-CNT"].IsContingency, pos["10-MAT"].IsContingency)
	}

	// A second ORIGINAL cannot be approved: changes after the baseline are REVISIONs.
	second, err := f.svc.CreateBudgetRevision(f.ctx, c.pm, f.project, "ORIGINAL", "Second baseline", []BudgetLine{{CostCodeID: b.mat, Amount: dec("5")}})
	must(t, "create second original", err)
	must(t, "submit second original", f.svc.SubmitBudgetRevision(f.ctx, c.pm, second))
	if err := f.svc.DecideBudgetRevision(f.ctx, c.commercial, second, true, "should fail"); err == nil {
		t.Fatal("a second approved ORIGINAL budget was accepted")
	}

	// REVISION: +150,000 on 10-MAT, -50,000 on 20-FIN.
	rev, err := f.svc.CreateBudgetRevision(f.ctx, c.pm, f.project, "REVISION", "Added basement scope", []BudgetLine{
		{CostCodeID: b.mat, Amount: dec("150000")}, {CostCodeID: b.fin, Amount: dec("-50000")}})
	must(t, "create revision", err)
	must(t, "submit revision", f.svc.SubmitBudgetRevision(f.ctx, c.pm, rev))

	// Maker/checker and human-only: the author and an AI principal are refused.
	if err := f.svc.DecideBudgetRevision(f.ctx, c.pm, rev, true, "self"); err == nil {
		t.Fatal("the author approved their own budget revision")
	}
	ai := auth.Principal{Kind: auth.KindAI, UserID: "u-ai", TenantID: f.tenant, Roles: []string{"commercial_manager"}, ProjectIDs: []string{f.project}}
	if err := f.svc.DecideBudgetRevision(f.ctx, ai, rev, true, "machine"); err == nil {
		t.Fatal("an AI principal approved a budget revision")
	}
	must(t, "approve revision", f.svc.DecideBudgetRevision(f.ctx, c.commercial, rev, true, "scope approved by client"))

	// Transfer 100,000 from 10-MAT to 20-FIN.
	trf, err := f.svc.RequestBudgetTransfer(f.ctx, c.pm, BudgetTransferRequest{ProjectID: f.project, FromCostCode: b.mat, ToCostCode: b.fin,
		Amount: dec("100000"), Reason: "Finishes specification upgraded"})
	must(t, "request transfer", err)
	must(t, "submit transfer", f.svc.SubmitBudgetTransfer(f.ctx, c.pm, trf))
	if err := f.svc.DecideBudgetTransfer(f.ctx, c.pm, trf, true, "self"); err == nil {
		t.Fatal("the requester approved their own transfer")
	}
	must(t, "approve transfer", f.svc.DecideBudgetTransfer(f.ctx, c.commercial, trf, true, "agreed"))

	// Drawdown 60,000 from contingency (source chosen automatically) to 10-MAT.
	drw, err := f.svc.RequestContingencyDrawdown(f.ctx, c.pm, ContingencyDrawdownRequest{ProjectID: f.project, ToCostCode: b.mat,
		Amount: dec("60000"), Reason: "Unforeseen ground conditions"})
	must(t, "request drawdown", err)
	must(t, "submit drawdown", f.svc.SubmitContingencyDrawdown(f.ctx, c.pm, drw))
	must(t, "approve drawdown", f.svc.DecideContingencyDrawdown(f.ctx, c.commercial, drw, true, "risk materialised"))

	pos = b.position(t)
	// 10-MAT: 1,000,000 + 150,000 - 100,000 + 60,000 = 1,110,000
	wantAmount(t, "10-MAT current", pos["10-MAT"].Current.String(), "1110000")
	wantAmount(t, "10-MAT original", pos["10-MAT"].Original.String(), "1000000")
	wantAmount(t, "10-MAT drawdowns in", pos["10-MAT"].DrawdownsIn.String(), "60000")
	// 20-FIN: 300,000 - 50,000 + 100,000 = 350,000
	wantAmount(t, "20-FIN current", pos["20-FIN"].Current.String(), "350000")
	// 90-CNT: 200,000 - 60,000 = 140,000 (no double counting)
	wantAmount(t, "90-CNT current", pos["90-CNT"].Current.String(), "140000")
	wantAmount(t, "90-CNT drawdowns out", pos["90-CNT"].DrawdownsOut.String(), "60000")

	// The procurement budget check reads the same current budget.
	var budget string
	must(t, "cost position", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT budget::text FROM p2p_cost_position($1::uuid, NULL, NULL)`, b.mat).Scan(&budget)
	}))
	wantAmount(t, "p2p_cost_position budget for 10-MAT", budget, "1110000")

	// Limits: a transfer beyond the source's available budget is refused at approval.
	big, err := f.svc.RequestBudgetTransfer(f.ctx, c.pm, BudgetTransferRequest{ProjectID: f.project, FromCostCode: b.fin, ToCostCode: b.mat,
		Amount: dec("350000.01"), Reason: "More than 20-FIN holds"})
	must(t, "request big transfer", err)
	must(t, "submit big transfer", f.svc.SubmitBudgetTransfer(f.ctx, c.pm, big))
	if err := f.svc.DecideBudgetTransfer(f.ctx, c.commercial, big, true, "should fail"); err == nil {
		t.Fatal("a transfer above the available budget was approved")
	}
	// ... a drawdown beyond the remaining contingency ...
	over, err := f.svc.RequestContingencyDrawdown(f.ctx, c.pm, ContingencyDrawdownRequest{ProjectID: f.project, FromCostCode: b.cnt, ToCostCode: b.fin,
		Amount: dec("140000.01"), Reason: "More than remains"})
	must(t, "request big drawdown", err)
	must(t, "submit big drawdown", f.svc.SubmitContingencyDrawdown(f.ctx, c.pm, over))
	if err := f.svc.DecideContingencyDrawdown(f.ctx, c.commercial, over, true, "should fail"); err == nil {
		t.Fatal("a drawdown above the remaining contingency was approved")
	}
	// ... and a drawdown from a code that is not contingency.
	notCnt, err := f.svc.RequestContingencyDrawdown(f.ctx, c.pm, ContingencyDrawdownRequest{ProjectID: f.project, FromCostCode: b.fin, ToCostCode: b.mat,
		Amount: dec("10"), Reason: "Not a contingency code"})
	must(t, "request drawdown from non-contingency", err)
	must(t, "submit drawdown from non-contingency", f.svc.SubmitContingencyDrawdown(f.ctx, c.pm, notCnt))
	if err := f.svc.DecideContingencyDrawdown(f.ctx, c.commercial, notCnt, true, "should fail"); err == nil {
		t.Fatal("a drawdown from a non-contingency code was approved")
	}
	// Rejection needs a reason and is final.
	if err := f.svc.DecideBudgetTransfer(f.ctx, c.commercial, big, false, ""); err == nil {
		t.Fatal("a rejection without a reason was accepted")
	}
	must(t, "reject big transfer", f.svc.DecideBudgetTransfer(f.ctx, c.commercial, big, false, "exceeds the finishes budget"))
	if err := f.svc.DecideBudgetTransfer(f.ctx, c.commercial, big, true, "second decision"); err == nil {
		t.Fatal("a rejected transfer was decided again")
	}
	_ = orig
}

// The database refuses forged or illegal changes made with raw SQL as the
// tenant's own login, without the kernel and without attestations.
func TestBudget_DatabaseRefusesBypass(t *testing.T) {
	b := newBudgetWorld(t)
	f, c := b.f, b.cast
	orig := b.approvedOriginal(t)

	draft, err := f.svc.CreateBudgetRevision(f.ctx, c.pm, f.project, "REVISION", "Draft revision", []BudgetLine{{CostCodeID: b.mat, Amount: dec("10")}})
	must(t, "draft revision", err)
	trf, err := f.svc.RequestBudgetTransfer(f.ctx, c.pm, BudgetTransferRequest{ProjectID: f.project, FromCostCode: b.mat, ToCostCode: b.fin, Amount: dec("1000"), Reason: "Probe transfer"})
	must(t, "probe transfer", err)
	must(t, "submit probe transfer", f.svc.SubmitBudgetTransfer(f.ctx, c.pm, trf))

	probes := []struct{ name, sql string }{
		{"DRAFT -> APPROVED directly", `UPDATE budget_versions SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '` + draft + `'`},
		{"approve a transfer without an attestation", `UPDATE budget_transfers SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '` + trf + `'`},
		// The amount column is not even updatable by the application role.
		{"change the amount of a submitted transfer", `UPDATE budget_transfers SET amount = 1 WHERE id = '` + trf + `'`},
		{"edit a line of an approved version", `UPDATE budget_lines SET amount = 999999999 WHERE version_id = '` + orig + `'`},
		{"delete a line of an approved version", `DELETE FROM budget_lines WHERE version_id = '` + orig + `'`},
		{"insert an already-approved version", `INSERT INTO budget_versions (project_id, kind, seq, reason, status, created_by, approved_by) VALUES ('` + f.project + `', 'REVISION', 99, 'forged', 'APPROVED', 'a', 'b')`},
		{"transfer to another project's code", `INSERT INTO budget_transfers (project_id, from_cost_code, to_cost_code, amount, currency, reason, requested_by)
			SELECT '` + f.project + `', '` + b.mat + `', id, 1, 'ETB', 'cross project', 'u-pm' FROM cost_codes WHERE project_id <> '` + f.project + `' LIMIT 1`},
	}
	for _, pr := range probes {
		err := f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			ct, err := tx.Exec(ctx, pr.sql)
			if err == nil && ct.RowsAffected() == 0 {
				return errors.New("no rows (refused by visibility)")
			}
			return err
		})
		if err == nil {
			t.Fatalf("database accepted: %s", pr.name)
		}
		t.Logf("held: %s: %v", pr.name, err)
	}

	// Tenant isolation: the other tenant sees nothing of this project's budget.
	var n int
	must(t, "tenant2 view read", f.app.WithTenant(f.ctx, f.tenant2, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM project_budget_current WHERE project_id = $1::uuid`, f.project).Scan(&n)
	}))
	if n != 0 {
		t.Fatalf("tenant2 read %d budget rows of tenant1 through project_budget_current", n)
	}
}

// Two transfers that each fit the available budget but not together: the
// project lock serialises them and the second approval is refused.
func TestBudget_ConcurrentTransfersCannotOverspend(t *testing.T) {
	b := newBudgetWorld(t)
	f, c := b.f, b.cast
	b.approvedOriginal(t)

	// 20-FIN holds 300,000. Two transfers of 200,000 each.
	ids := make([]string, 2)
	for i := range ids {
		id, err := f.svc.RequestBudgetTransfer(f.ctx, c.pm, BudgetTransferRequest{ProjectID: f.project, FromCostCode: b.fin, ToCostCode: b.mat,
			Amount: dec("200000"), Reason: "Race candidate"})
		must(t, "request", err)
		must(t, "submit", f.svc.SubmitBudgetTransfer(f.ctx, c.pm, id))
		ids[i] = id
	}
	approvers := []auth.Principal{c.commercial, c.cfo}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = f.svc.DecideBudgetTransfer(f.ctx, approvers[i], ids[i], true, "race")
		}(i)
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, e := range errs {
		if e == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("expected exactly one approval to win, got %d (errors: %v)", ok, errs)
	}
	pos := b.position(t)
	wantAmount(t, "20-FIN after race", pos["20-FIN"].Current.String(), "100000")
}
