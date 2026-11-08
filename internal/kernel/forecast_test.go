package kernel

// Tests for monthly cost forecasting (WP-2.1, migration 0044, kernel/forecast.go).
// What is proven:
//  1. the three methods compute ETC / EAC / VAC from the real cost position,
//     and the database freezes those figures (a client cannot set them);
//  2. a forecast above budget must be explained before it can be submitted;
//  3. maker/checker, fresh MFA, human-only decision, attestation;
//  4. an approved forecast is immutable; one open cycle per project and one
//     approved cycle per month end;
//  5. scope: a manager outside the project cannot prepare or read it.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func TestForecast_MethodsAndGuards(t *testing.T) {
	b := newBudgetWorld(t)
	f, c := b.f, b.cast
	b.approvedOriginal(t) // 10-MAT 1,000,000; 20-FIN 300,000; 90-CNT 200,000

	period := time.Now().Format("2006-01-02")
	outsider := auth.Principal{Kind: auth.KindHuman, UserID: "u-pm-out", TenantID: f.tenant, Roles: []string{"project_manager"}, ProjectIDs: []string{"00000000-0000-0000-0000-000000000001"}}
	if _, err := f.svc.CreateForecast(f.ctx, outsider, f.project, period, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a manager outside the project prepared a forecast: %v", err)
	}
	if _, err := f.svc.CreateForecast(f.ctx, c.estimator, f.project, period, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an estimator prepared a forecast: %v", err)
	}
	if _, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, "next-month", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad date: %v", err)
	}
	if _, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, time.Now().AddDate(0, 3, 0).Format("2006-01-02"), ""); err == nil {
		t.Fatal("a forecast for a future period was accepted")
	}

	id, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, period, "Month-end forecast")
	must(t, "create", err)
	if _, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, period, ""); err == nil {
		t.Fatal("two open cycles on one project")
	}
	fc, err := f.svc.GetForecast(f.ctx, c.pm, id)
	must(t, "get", err)
	line := func(code string) ForecastLine {
		fc, err := f.svc.GetForecast(f.ctx, c.pm, id)
		must(t, "get", err)
		for _, l := range fc.Lines {
			if l.CostCode == code {
				return l
			}
		}
		t.Fatalf("no line for %s", code)
		return ForecastLine{}
	}
	if len(fc.Lines) != 3 || fc.Status != "DRAFT" {
		t.Fatalf("new cycle: %+v", fc)
	}
	// REMAINING_BUDGET with nothing spent: EAC equals the budget, VAC is zero.
	mat := line("10-MAT")
	wantAmount(t, "mat budget", mat.Budget.String(), "1000000")
	wantAmount(t, "mat etc", mat.ETC.String(), "1000000")
	wantAmount(t, "mat eac", mat.EAC.String(), "1000000")
	wantAmount(t, "mat vac", mat.VAC.String(), "0")

	// MANUAL above the budget: figures follow, but it cannot be submitted unexplained.
	over := dec("1200000")
	must(t, "manual", f.svc.SetForecastLine(f.ctx, c.pm, id, b.mat, "MANUAL", &over, ""))
	mat = line("10-MAT")
	wantAmount(t, "manual eac", mat.EAC.String(), "1200000")
	wantAmount(t, "manual vac", mat.VAC.String(), "-200000")
	if err := f.svc.SubmitForecast(f.ctx, c.pm, id); err == nil {
		t.Fatal("an over-budget forecast was submitted without an explanation")
	}
	must(t, "explain", f.svc.SetForecastLine(f.ctx, c.pm, id, b.mat, "MANUAL", &over, "Steel price rise confirmed by supplier circular 14"))
	if err := f.svc.SetForecastLine(f.ctx, c.pm, id, b.mat, "MANUAL", nil, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("MANUAL without an amount: %v", err)
	}
	if err := f.svc.SetForecastLine(f.ctx, c.pm, id, b.mat, "GUESS", nil, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown method: %v", err)
	}
	// OPEN_COMMITMENTS with no orders: nothing more to spend.
	must(t, "commitments", f.svc.SetForecastLine(f.ctx, c.pm, id, b.fin, "OPEN_COMMITMENTS", nil, ""))
	wantAmount(t, "fin etc", line("20-FIN").ETC.String(), "0")

	// The database computes the figures; a client cannot write them.
	rawFails(t, f, "forge a forecast figure", `UPDATE forecast_lines SET eac = 1 WHERE cycle_id = '`+id+`'`)
	rawFails(t, f, "move a line to another cycle", `UPDATE forecast_lines SET cycle_id = gen_random_uuid() WHERE cycle_id = '`+id+`'`)
	rawFails(t, f, "insert a cycle already approved", `INSERT INTO forecast_cycles (project_id, period_end, status, created_by, approved_by) VALUES ('`+f.project+`', current_date - 40, 'APPROVED', 'x', 'y')`)

	// Only the preparer edits; the same person cannot decide.
	other := c.pm
	other.UserID = "u-pm-other"
	if err := f.svc.SetForecastLine(f.ctx, other, id, b.fin, "MANUAL", &over, "x"); err == nil {
		t.Fatal("someone else edited the preparer's draft")
	}
	must(t, "submit", f.svc.SubmitForecast(f.ctx, c.pm, id))
	if err := f.svc.SetForecastLine(f.ctx, c.pm, id, b.fin, "OPEN_COMMITMENTS", nil, ""); err == nil {
		t.Fatal("a submitted forecast was edited")
	}
	rawFails(t, f, "edit a submitted line", `UPDATE forecast_lines SET note = 'edited' WHERE cycle_id = '`+id+`'`)
	if err := f.svc.DecideForecast(f.ctx, c.pm, id, true, ""); err == nil {
		t.Fatal("the preparer decided their own forecast")
	}
	stale := c.cfo
	stale.AuthTime = time.Now().Add(-time.Hour)
	if err := f.svc.DecideForecast(f.ctx, stale, id, true, ""); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("approval without fresh MFA: %v", err)
	}
	ai := c.cfo
	ai.Kind = auth.KindAI
	if err := f.svc.DecideForecast(f.ctx, ai, id, true, ""); err == nil {
		t.Fatal("an AI principal decided a forecast")
	}
	rawFails(t, f, "approve without an attestation", `UPDATE forecast_cycles SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '`+id+`'`)
	if err := f.svc.DecideForecast(f.ctx, c.cfo, id, false, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("rejection without a reason: %v", err)
	}
	must(t, "approve", f.svc.DecideForecast(f.ctx, c.cfo, id, true, ""))
	fc, err = f.svc.GetForecast(f.ctx, c.cfo, id)
	must(t, "get approved", err)
	if fc.Status != "APPROVED" || fc.ApprovedBy != c.cfo.UserID {
		t.Fatalf("after approval: %+v", fc)
	}
	wantAmount(t, "total eac", fc.EAC.String(), "1400000") // 1,200,000 manual + 0 commitments + 200,000 remaining contingency
	wantAmount(t, "total vac", fc.VAC.String(), "100000")  // 1,500,000 budget - 1,400,000
	rawFails(t, f, "edit an approved forecast", `UPDATE forecast_cycles SET note = 'edited' WHERE id = '`+id+`'`)
	rawFails(t, f, "edit an approved line", `UPDATE forecast_lines SET method = 'MANUAL', manual_etc = 0 WHERE cycle_id = '`+id+`'`)

	// A new cycle for the same month end may be prepared and rejected, but not approved twice.
	id2, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, period, "")
	must(t, "second cycle", err)
	must(t, "submit 2", f.svc.SubmitForecast(f.ctx, c.pm, id2))
	if err := f.svc.DecideForecast(f.ctx, c.cfo, id2, true, ""); err == nil {
		t.Fatal("two approved forecasts for one month end")
	}
	must(t, "reject 2", f.svc.DecideForecast(f.ctx, c.cfo, id2, false, "Duplicate of the approved forecast"))

	// An abandoned draft is cancelled by its author, with a reason, and frees the project.
	id3, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, period, "")
	must(t, "third cycle", err)
	if _, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, period, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a second open forecast: want a plain refusal, got %v", err)
	}
	if err := f.svc.CancelForecast(f.ctx, c.pm, id3, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("cancel without a reason: %v", err)
	}
	if err := f.svc.CancelForecast(f.ctx, other, id3, "not mine to cancel"); err == nil {
		t.Fatal("someone else cancelled the preparer's draft")
	}
	must(t, "cancel", f.svc.CancelForecast(f.ctx, c.pm, id3, "Prepared in error"))
	rawFails(t, f, "revive a cancelled forecast", `UPDATE forecast_cycles SET status = 'DRAFT' WHERE id = '`+id3+`'`)
	if err := f.svc.CancelForecast(f.ctx, c.pm, id, "too late"); err == nil {
		t.Fatal("an approved forecast was cancelled")
	}
	_, err = f.svc.CreateForecast(f.ctx, c.pm, f.project, period, "")
	must(t, "a new forecast after cancelling", err)
}

// A project with no money on any cost code has nothing to forecast.
func TestForecast_EmptyProjectRefused(t *testing.T) {
	b := newBudgetWorld(t)
	if _, err := b.f.svc.CreateForecast(b.f.ctx, b.cast.pm, b.f.project, time.Now().Format("2006-01-02"), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a forecast for a project with no budget: %v", err)
	}
}

// The WIP schedule and the forecast summary read the latest APPROVED forecast.
// Percent complete is cost to date / EAC, earned revenue is contract value x
// that, and over/under billing is billed - earned; each figure is recomputed
// here from first principles and compared with the report.
func TestForecast_WIPScheduleAndSummary(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	// Posted project cost: a received and invoiced purchase order, posted by finance.
	_, _, po := w.awardedPO(t, "20", "500")
	_, j1 := w.receipt(t, po, GRNLine{POLineNo: 1, QtyReceived: dec("12")}, GRNLine{POLineNo: 2, QtyReceived: dec("500")})
	inv1, _ := w.invoice(t, InvoiceInput{POID: po, InvoiceNumber: "WIP/2026/0001", InvoiceDate: today(), DueDate: today().AddDate(0, 0, 30),
		Subtotal: dec("1486500"), TaxAmount: dec("222975"), Total: dec("1709475"),
		Lines: []InvoiceLine{{1, dec("12"), dec("84500")}, {2, dec("500"), dec("945")}}})
	j3, err := f.svc.ApproveInvoice(f.ctx, c.apSup, inv1, "")
	must(t, "approve invoice", err)
	w.financePost(t, j1, j3)
	// Billing: an active contract, a measured and certified IPC, an approved customer invoice.
	w.activeContract(t)
	w.measure(t, 15, "4000", "300", "20")
	_, _ = w.approvedInvoice(t, w.certifiedIPC(t, 16, "ERA/IPC/01"))

	// Before any approved forecast the reports are empty.
	// (A project with no active contract shows no expected loss: covered by the CASE in the report and checked on the demo.)
	run := func(id string) ReportResult {
		r, err := f.svc.RunReport(f.ctx, c.cfo, id, ReportFilters{EntityID: f.entity, ProjectID: f.project})
		must(t, id, err)
		return r
	}
	if r := run("wip-schedule"); len(r.Rows) != 0 {
		t.Fatalf("WIP before a forecast: %v", r.Rows)
	}
	id, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, today().Format("2006-01-02"), "")
	must(t, "forecast", err)
	if len(run("forecast-summary").Rows) != 0 {
		t.Fatal("a draft forecast appears in the summary")
	}
	must(t, "submit", f.svc.SubmitForecast(f.ctx, c.pm, id))
	must(t, "approve", f.svc.DecideForecast(f.ctx, c.cfo, id, true, ""))
	fc, err := f.svc.GetForecast(f.ctx, c.cfo, id)
	must(t, "get", err)
	if fc.CostToDate.IsZero() {
		t.Fatal("the test has no posted cost, so percent complete would be untested")
	}

	var contractValue, billed decimal.Decimal
	must(t, "figures", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT sum(contract_value) FROM contracts WHERE project_id = $1::uuid AND status = 'ACTIVE'`, f.project).Scan(&contractValue); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT sum(gross) FROM customer_invoices WHERE project_id = $1::uuid AND status = 'APPROVED'`, f.project).Scan(&billed)
	}))
	ratio := decimal.Min(fc.CostToDate.Div(fc.EAC), decimal.NewFromInt(1))
	earned := contractValue.Mul(ratio).Round(2)

	rows := run("wip-schedule").Rows
	if len(rows) != 1 {
		t.Fatalf("WIP rows: %v", rows)
	}
	r := rows[0]
	for k, want := range map[string]decimal.Decimal{"contract_value": contractValue, "eac": fc.EAC, "cost_to_date": fc.CostToDate, "earned_revenue": earned, "billed": billed, "over_under": billed.Sub(earned)} {
		if !dec(r[k]).Equal(want) {
			t.Fatalf("WIP %s: report %v, want %s", k, r[k], want)
		}
	}
	if want := decimal.Max(fc.EAC.Sub(contractValue), decimal.Zero); !dec(r["expected_loss"]).Equal(want) {
		t.Fatalf("expected loss %v, want %s", r["expected_loss"], want)
	}
	if want := ratio.Mul(decimal.NewFromInt(100)).Round(2).String() + "%"; r["percent_complete"] != want {
		t.Fatalf("percent complete %v, want %s", r["percent_complete"], want)
	}
	sum := run("forecast-summary")
	if len(sum.Rows) != len(fc.Lines) || !dec(sum.Totals["eac"]).Equal(fc.EAC) || !dec(sum.Totals["vac"]).Equal(fc.VAC) {
		t.Fatalf("forecast summary: %d rows totals %v; forecast has %d lines EAC %s VAC %s", len(sum.Rows), sum.Totals, len(fc.Lines), fc.EAC, fc.VAC)
	}
}
