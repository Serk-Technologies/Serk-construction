package kernel

// Tests for period-end (WP-2.13, migration 0058, kernel/periodend.go).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

// EFY 2019 runs 1 Hamle 2018 (8 July 2026) to 30 Sene 2019 (7 July 2027);
// Nehase carries Pagume (5 days); EFY 2016 (a year whose Meskerem 1 fell on
// 12 September 2023) checks the leap rule.
func TestEthiopianFiscalYear(t *testing.T) {
	y := EthiopianFiscalYear(2019)
	if len(y) != 12 || y[0].Name != "EFY2019-01 Hamle" || y[0].Start != "2026-07-08" || y[0].End != "2026-08-06" {
		t.Fatalf("Hamle: %+v", y[0])
	}
	if y[1].Start != "2026-08-07" || y[1].End != "2026-09-10" || y[2].Start != "2026-09-11" || y[11].End != "2027-07-07" {
		t.Fatalf("EFY 2019: %+v", y)
	}
	for i := 1; i < 12; i++ {
		prev, _ := time.Parse("2006-01-02", y[i-1].End)
		cur, _ := time.Parse("2006-01-02", y[i].Start)
		if !cur.Equal(prev.AddDate(0, 0, 1)) {
			t.Fatalf("gap between %s and %s", y[i-1].Name, y[i].Name)
		}
	}
	if m := meskerem1(2016); m.Format("2006-01-02") != "2023-09-12" {
		t.Fatalf("Meskerem 1, 2016: %s", m.Format("2006-01-02"))
	}
	if g := GregorianFiscalYear(2028); g[1].End != "2028-02-29" || g[11].End != "2028-12-31" {
		t.Fatalf("Gregorian 2028: %+v", g)
	}
}

func TestPeriodEnd_FiscalYearRecurringAccrualOverhead(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	must(t, "accounts", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, a := range []struct{ code, name, typ string }{{"6200", "Head office overhead", "EXPENSE"}, {"6210", "Overhead applied", "EXPENSE"},
			{"6300", "Rent", "EXPENSE"}, {"2400", "Accrued expenses", "LIABILITY"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: f.tenant, LegalEntityID: f.entity, Code: a.code, Name: a.name, Type: ledger.AccountType(a.typ), Active: true})
			if err != nil {
				return err
			}
			w.acct[a.code] = id
		}
		return nil
	}))

	// Fiscal years: the controller generates; an overlap is refused in plain words.
	if _, err := f.svc.GenerateFiscalYear(f.ctx, c.pm, f.entity, "ETHIOPIAN", 2019); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager generated periods: %v", err)
	}
	if _, err := f.svc.GenerateFiscalYear(f.ctx, c.controller, f.entity, "ETHIOPIAN", 2019); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a year overlapping FY2026-Q4 was accepted: %v", err)
	}
	specs, err := f.svc.GenerateFiscalYear(f.ctx, c.controller, f.entity, "ETHIOPIAN", 2021)
	must(t, "EFY 2021", err)
	ps, err := f.svc.FiscalPeriods(f.ctx, c.controller, f.entity)
	must(t, "periods", err)
	if len(specs) != 12 || len(ps) < 13 {
		t.Fatalf("periods: %d generated, %d listed", len(specs), len(ps))
	}

	// Recurring rent: 30,000 on the 5th, from October; runs October and November once each.
	rent := []JournalLineInput{{AccountID: w.acct["6300"], Side: "DEBIT", Amount: dec("30000")}, {AccountID: w.acct["2400"], Side: "CREDIT", Amount: dec("30000")}}
	if _, err := f.svc.CreateRecurring(f.ctx, c.accountant, RecurringInput{LegalEntityID: f.entity, Name: "Yard rent", FirstRunOn: "2026-10-05",
		Lines: []JournalLineInput{rent[0], {AccountID: w.acct["2400"], Side: "CREDIT", Amount: dec("29000")}}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("an unbalanced template was accepted: %v", err)
	}
	tpl, err := f.svc.CreateRecurring(f.ctx, c.accountant, RecurringInput{LegalEntityID: f.entity, Name: "Yard rent", FirstRunOn: "2026-10-05", EndsOn: "2026-11-30", Lines: rent})
	must(t, "template", err)
	made, err := f.svc.RunRecurring(f.ctx, c.accountant, "2026-11-08")
	must(t, "run", err)
	if len(made) != 2 {
		t.Fatalf("recurring journals created: %v", made)
	}
	if again, err := f.svc.RunRecurring(f.ctx, c.accountant, "2026-11-08"); err != nil || len(again) != 0 {
		t.Fatalf("second run: %v %v", again, err)
	}
	rawFails(t, f, "move a template back", `UPDATE recurring_journals SET next_run_on = '2026-10-05' WHERE id = '`+tpl+`'`)
	w.financePost(t, made...)

	// Accrual dated 31 October reversing on 1 November.
	acc, err := f.svc.CreateAccrual(f.ctx, c.accountant, AccrualInput{LegalEntityID: f.entity, Description: "Unbilled consultancy October", Date: "2026-10-31", ReverseOn: "2026-11-01",
		Lines: []JournalLineInput{{AccountID: w.acct["6200"], Side: "DEBIT", Amount: dec("12000")}, {AccountID: w.acct["2400"], Side: "CREDIT", Amount: dec("12000")}}})
	must(t, "accrual", err)
	if rev, _ := f.svc.RunAccrualReversals(f.ctx, c.controller, "2026-11-08"); len(rev) != 0 {
		t.Fatalf("a draft accrual was reversed: %v", rev)
	}
	w.financePost(t, acc)
	rev, err := f.svc.RunAccrualReversals(f.ctx, c.controller, "2026-11-08")
	must(t, "reversals", err)
	if len(rev) != 1 {
		t.Fatalf("reversals: %v", rev)
	}
	if again, _ := f.svc.RunAccrualReversals(f.ctx, c.controller, "2026-11-08"); len(again) != 0 {
		t.Fatalf("an accrual was reversed twice: %v", again)
	}
	var revDate string
	must(t, "reversal date", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT (business_event_at AT TIME ZONE 'UTC')::date::text FROM journals WHERE id = $1::uuid AND reversal_of = $2::uuid`, rev[0], acc).Scan(&revDate)
	}))
	if revDate != "2026-11-01" {
		t.Fatalf("reversal dated %s", revDate)
	}

	// Overhead: the pool (6200: 12,000 accrual posted on 31 October) is spread by
	// posted direct cost (the project's 60,000 of rent has no project, so a
	// project cost is posted first: 50,000 on the project).
	direct := []JournalLineInput{{AccountID: w.acct["5000"], Side: "DEBIT", Amount: dec("50000")}, {AccountID: w.acct["2400"], Side: "CREDIT", Amount: dec("50000")}}
	j, err := f.svc.journalOf(f.ctx, c.accountant, f.entity, f.project, "Site cost", "MANUAL", "", "test-direct-cost", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), direct)
	must(t, "direct journal", err)
	res, err := f.svc.CreateJournal(f.ctx, c.accountant, j)
	must(t, "direct cost", err)
	w.financePost(t, res.JournalID)
	in := OverheadInput{LegalEntityID: f.entity, From: "2026-10-01", To: "2026-10-31", PoolAccount: w.acct["6200"], TargetAccount: w.acct["6210"]}
	sh, err := f.svc.PreviewOverhead(f.ctx, c.accountant, in)
	must(t, "preview", err)
	if len(sh) != 1 || !sh[0].Share.Equal(dec("12000")) || !sh[0].Driver.Equal(dec("50000")) {
		t.Fatalf("overhead shares: %+v", sh)
	}
	run, err := f.svc.AllocateOverhead(f.ctx, c.accountant, in)
	must(t, "allocate", err)
	if _, err := f.svc.AllocateOverhead(f.ctx, c.accountant, in); err == nil {
		t.Fatal("the same pool and range were allocated twice")
	}
	rawFails(t, f, "forge an overhead share", `INSERT INTO overhead_lines (run_id, project_id, driver, share) VALUES ('`+run+`', '`+f.project+`', 1, 1)`)
	view, err := f.svc.PeriodEndView(f.ctx, c.accountant)
	must(t, "view", err)
	if len(view["recurring"].([]map[string]any)) != 1 || len(view["accruals"].([]map[string]any)) != 1 || len(view["overhead"].([]map[string]any)) != 1 {
		t.Fatalf("view: %v", view)
	}
}
