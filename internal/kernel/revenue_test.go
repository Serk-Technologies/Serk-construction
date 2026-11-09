package kernel

// Tests for revenue over time and the WIP adjustment journal (WP-2.2/2.3,
// migration 0053, kernel/revenue.go). Proven: the revenue policy is
// proposed and approved by two people with typed accounts; the database
// computes earned, billed and the adjustment per contract; the run's journal
// equals that accounting, is approved and posted by finance, cannot be edited
// by another transaction, and a draft run is cancelled with its journal; the
// next run moves only what changed since the last posted run.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

func TestRevenue_RunAndWIPJournal(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	acc := auth.Principal{Kind: auth.KindHuman, UserID: "u-proj-acct", TenantID: f.tenant, Roles: []string{"project_accountant"}, ProjectIDs: []string{f.project}, IssuedAt: time.Now().Add(-time.Minute)}
	must(t, "accounts", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, a := range []struct{ code, name, typ string }{{"1160", "Contract assets", "ASSET"}, {"2260", "Contract liabilities", "LIABILITY"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: f.tenant, LegalEntityID: f.entity, Code: a.code, Name: a.name, Type: ledger.AccountType(a.typ), Active: true})
			if err != nil {
				return err
			}
			w.acct[a.code] = id
		}
		return nil
	}))

	// Cost, a forecast and billing: cost-to-cost progress against an invoice.
	_, _, po := w.awardedPO(t, "20", "500")
	_, j1 := w.receipt(t, po, GRNLine{POLineNo: 1, QtyReceived: dec("12")}, GRNLine{POLineNo: 2, QtyReceived: dec("500")})
	inv, _ := w.invoice(t, InvoiceInput{POID: po, InvoiceNumber: "REV/2026/0001", InvoiceDate: today(), DueDate: today().AddDate(0, 0, 30),
		Subtotal: dec("1486500"), TaxAmount: dec("222975"), Total: dec("1709475"), Lines: []InvoiceLine{{1, dec("12"), dec("84500")}, {2, dec("500"), dec("945")}}})
	j3, err := f.svc.ApproveInvoice(f.ctx, c.apSup, inv, "")
	must(t, "approve invoice", err)
	w.financePost(t, j1, j3)
	w.activeContract(t)
	w.measure(t, 15, "4000", "300", "20")
	_, arj := w.approvedInvoice(t, w.certifiedIPC(t, 16, "ERA/IPC/R1"))
	w.financePost(t, arj)
	fcID, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, today().Format("2006-01-02"), "")
	must(t, "forecast", err)
	must(t, "forecast submit", f.svc.SubmitForecast(f.ctx, c.pm, fcID))
	must(t, "forecast approve", f.svc.DecideForecast(f.ctx, c.cfo, fcID, true, ""))
	fc, err := f.svc.GetForecast(f.ctx, c.cfo, fcID)
	must(t, "get forecast", err)
	period := today().Format("2006-01-02")

	// Policy: no run without one; typed accounts; maker/checker.
	if _, err := f.svc.PrepareRevenueRun(f.ctx, acc, f.project, period); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a run without a revenue policy: %v", err)
	}
	if _, err := f.svc.ProposeRevenuePolicy(f.ctx, c.controller, RevenuePolicyInput{LegalEntityID: f.entity, Method: "INPUT_COST", ContractAssetAcct: w.acct["2260"], ContractLiabilityAc: w.acct["1160"]}); err == nil {
		t.Fatal("a liability account was accepted as the contract asset")
	}
	pol, err := f.svc.ProposeRevenuePolicy(f.ctx, c.controller, RevenuePolicyInput{LegalEntityID: f.entity, Method: "input_cost", ContractAssetAcct: w.acct["1160"], ContractLiabilityAc: w.acct["2260"]})
	must(t, "policy", err)
	both := auth.Principal{Kind: auth.KindHuman, UserID: "u-controller", TenantID: f.tenant, Roles: []string{"cfo"}}
	if err := f.svc.DecideRevenuePolicy(f.ctx, w.mfa(both), pol, true, ""); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("the proposer approved the policy: %v", err)
	}
	noMFA := c.cfo
	noMFA.MFA = false
	if err := f.svc.DecideRevenuePolicy(f.ctx, noMFA, pol, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("approval without fresh MFA: %v", err)
	}
	must(t, "approve policy", f.svc.DecideRevenuePolicy(f.ctx, w.mfa(c.cfo), pol, true, ""))
	if ps, err := f.svc.RevenuePolicies(f.ctx, c.cfo); err != nil || len(ps) != 1 || ps[0]["status"] != "APPROVED" || ps[0]["method"] != "INPUT_COST" {
		t.Fatalf("policies: %v %v", err, ps)
	}
	if d, err := f.svc.ListP2PDocs(f.ctx, c.cfo, "revenue_policy", "", 50); err != nil || len(d) != 1 {
		t.Fatalf("policy as a document: %v %v", err, d)
	}

	// Preview: earned = contract value x min(cost / EAC, 1); billed = the invoice's gross.
	if _, err := f.svc.PreviewRevenue(f.ctx, w.ar.qs, f.project, period); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a quantity surveyor (no ledger access) previewed the revenue run: %v", err)
	}
	if _, err := f.svc.PrepareRevenueRun(f.ctx, c.cfo, f.project, period); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the CFO prepared a revenue run: %v", err)
	}
	pv, err := f.svc.PreviewRevenue(f.ctx, acc, f.project, period)
	must(t, "preview", err)
	progress := decimal.Min(fc.CostToDate.Div(fc.EAC).Round(8), decimal.NewFromInt(1))
	earned := dec("23060000").Mul(progress).Round(2)
	if len(pv.Lines) != 1 || !pv.Lines[0].Progress.Equal(progress) || !pv.Lines[0].Earned.Equal(earned) || !pv.Lines[0].Billed.Equal(dec("5310000")) ||
		!pv.Lines[0].Cumulative.Equal(earned.Sub(dec("5310000"))) || !pv.Lines[0].Previous.IsZero() {
		t.Fatalf("preview: %+v (progress %s earned %s)", pv.Lines, progress, earned)
	}
	cum := pv.Lines[0].Cumulative
	t.Logf("progress %s, earned %s, billed 5,310,000, adjustment %s", progress, earned, cum)

	// The run and its journal.
	run, err := f.svc.PrepareRevenueRun(f.ctx, acc, f.project, period)
	must(t, "run", err)
	if _, err := f.svc.PrepareRevenueRun(f.ctx, acc, f.project, period); err == nil {
		t.Fatal("a second run while the first journal is a draft")
	}
	runs, err := f.svc.RevenueRuns(f.ctx, acc, f.project)
	must(t, "runs", err)
	if len(runs) != 1 || runs[0].JournalStatus != "DRAFT" || runs[0].JournalID == "" {
		t.Fatalf("runs: %+v", runs)
	}
	jid := runs[0].JournalID
	var lines string
	must(t, "journal lines", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT string_agg(a.code || ' ' || l.side || ' ' || l.amount::numeric(24,2), ', ' ORDER BY a.code) FROM journal_lines l JOIN accounts a ON a.id = l.account_id WHERE l.journal_id = $1::uuid`, jid).Scan(&lines)
	}))
	amt := cum.Abs().StringFixed(2)
	want := "1160 DEBIT " + amt + ", 4000 CREDIT " + amt
	if cum.IsNegative() {
		want = "2260 CREDIT " + amt + ", 4000 DEBIT " + amt
	}
	if lines != want {
		t.Fatalf("journal lines %q, want %q", lines, want)
	}
	rawFails(t, f, "edit a run journal's line", `UPDATE journal_lines SET amount = amount + 1 WHERE journal_id = '`+jid+`'`)
	rawFails(t, f, "forge a revenue line", `INSERT INTO revenue_lines (run_id, contract_id, contract_value, measure, measure_total, progress, earned, billed, cumulative, previous, movement)
		VALUES ('`+run+`', '`+w.contract+`', 1, 1, 1, 1, 1, 0, 1, 0, 1)`)

	// Cancel the draft (the journal goes with it), run again, finance posts.
	if err := f.svc.CancelRevenueRun(f.ctx, c.finMgr, run, ""); err == nil {
		t.Fatal("a run was cancelled without a reason")
	}
	if err := f.svc.CancelRevenueRun(f.ctx, acc, run, "Forecast to be revised first"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the preparer cancelled the run: %v", err)
	}
	must(t, "cancel", f.svc.CancelRevenueRun(f.ctx, c.finMgr, run, "Forecast to be revised first"))
	var js string
	must(t, "journal status", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM journals WHERE id = $1::uuid`, jid).Scan(&js)
	}))
	if js != "CANCELLED" {
		t.Fatalf("journal of a cancelled run is %s", js)
	}
	run2, err := f.svc.PrepareRevenueRun(f.ctx, acc, f.project, period)
	must(t, "run 2", err)
	runs, _ = f.svc.RevenueRuns(f.ctx, acc, f.project)
	w.financePost(t, runs[0].JournalID)
	if err := f.svc.CancelRevenueRun(f.ctx, c.finMgr, run2, "too late to cancel"); err == nil {
		t.Fatal("a run with a posted journal was cancelled")
	}
	runs, _ = f.svc.RevenueRuns(f.ctx, acc, f.project)
	if runs[0].JournalStatus != "POSTED" || (cum.IsNegative() && !runs[0].ContractLiability.Equal(cum.Neg())) || (cum.IsPositive() && !runs[0].ContractAsset.Equal(cum)) {
		t.Fatalf("after posting: %+v", runs[0])
	}

	// Nothing changed since the posted run: nothing to adjust.
	pv, err = f.svc.PreviewRevenue(f.ctx, acc, f.project, period)
	must(t, "preview 2", err)
	if !pv.Lines[0].Previous.Equal(cum) || !pv.Lines[0].Movement.IsZero() {
		t.Fatalf("after posting, preview: %+v", pv.Lines)
	}
	if _, err := f.svc.PrepareRevenueRun(f.ctx, acc, f.project, period); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a run with nothing to adjust: %v", err)
	}
	if ps, err := f.svc.RevenueProjects(f.ctx, acc); err != nil || len(ps) != 1 || ps[0]["id"] != f.project {
		t.Fatalf("revenue projects: %v %v", err, ps)
	}
	outsider := auth.Principal{Kind: auth.KindHuman, UserID: "u-acct-out", TenantID: f.tenant, Roles: []string{"project_accountant"}, ProjectIDs: []string{"00000000-0000-0000-0000-000000000001"}}
	if _, err := f.svc.RevenueRuns(f.ctx, outsider, f.project); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an outsider listed the runs: %v", err)
	}
}
