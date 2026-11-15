package kernel

// Tests for consolidation and intercompany (WP-2.10, migration 0062, kernel/consolidation.go).
//
// Golden fixture (hand-checked). Group SERK, presentation ETB, rates KES closing 0.45, average 0.44.
//   A  SERK ETHIOPIA PLC (ETB, parent, FULL 100%):
//      Dr 1000 1,000,000 / Cr 3000 1,000,000; Dr 1400 (due from Kenya) 90,000 / Cr 4100 (fee income) 90,000.
//   B  SERK KENYA LTD (KES, FULL 80%):
//      Dr 1000 500,000 / Cr 3000 500,000; Dr 6400 (fee expense) 200,000 / Cr 2400 (due to Ethiopia) 200,000;
//      in Q3 (before the period): Dr 1000 100,000 / Cr 4000 100,000.
//      Translated: 1000 270,000; 2400 -90,000; 3000 -225,000; 6400 88,000 (average); RE-BF -45,000;
//      sum -2,000 -> CTA +2,000. Net assets 180,000 -> NCI 36,000.
//   C  ADDIS ASSOCIATE (ETB, EQUITY 25%): Dr 1000 400,000 / Cr 3000 400,000 -> investment 100,000.
//   Eliminations: 1400 -90,000, 2400 +90,000 (balances match); 4100 +90,000, 6400 -88,000 ->
//   unmatched result difference -2,000 (fee translated at the average rate).
//   Consolidated: assets 1,370,000 (cash 1,270,000 + investment 100,000); liabilities 0;
//   equity 1,368,000 (3000 1,225,000, RE-BF 45,000, CTA -2,000, NCI 36,000, NCI-EQ -36,000,
//   associates 100,000); suspense -2,000; profit 0.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/ledger"
	"github.com/serk-group/serk-construction/pkg/money"
)

func TestConsolidation_TranslateEliminateApprove(t *testing.T) {
	f := newFixture(t)
	ent := map[string]string{"A": f.entity}
	acct := map[string]string{}
	must(t, "entities", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if ent["B"], err = postgres.CreateLegalEntity(ctx, tx, f.tenant, "SERK KENYA LTD", "KE", "KES", "KES"); err != nil {
			return err
		}
		if ent["C"], err = postgres.CreateLegalEntity(ctx, tx, f.tenant, "ADDIS ASSOCIATE", "ET", "ETB", "ETB"); err != nil {
			return err
		}
		for _, e := range []string{"B", "C"} {
			if _, err := postgres.CreateFiscalPeriod(ctx, tx, f.tenant, ent[e], "FY2026-Q4", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)); err != nil {
				return err
			}
		}
		if _, err := postgres.CreateFiscalPeriod(ctx, tx, f.tenant, ent["B"], "FY2026-Q3", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)); err != nil {
			return err
		}
		for _, a := range []struct{ e, code, name, typ string }{
			{"A", "1400", "Due from Serk Kenya", "ASSET"}, {"A", "3000", "Share capital", "EQUITY"}, {"A", "4100", "Management fee income", "REVENUE"},
			{"B", "1000", "Cash at bank", "ASSET"}, {"B", "2400", "Due to Serk Ethiopia", "LIABILITY"}, {"B", "3000", "Share capital", "EQUITY"},
			{"B", "4000", "Contract revenue", "REVENUE"}, {"B", "6400", "Management fee expense", "EXPENSE"},
			{"C", "1000", "Cash at bank", "ASSET"}, {"C", "3000", "Share capital", "EQUITY"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: f.tenant, LegalEntityID: ent[a.e], Code: a.code, Name: a.name, Type: ledger.AccountType(a.typ), Active: true})
			if err != nil {
				return err
			}
			acct[a.e+a.code] = id
		}
		acct["A1000"] = f.accounts["1000"]
		return nil
	}))
	post := func(e, ccy string, day time.Time, dr, cr, amount string) {
		t.Helper()
		f.lifecycle(t, ledger.Journal{LegalEntityID: ent[e], FunctionalCurrency: money.Currency(ccy), BusinessEventAt: day, Description: "Consolidation fixture",
			SourceType: "MANUAL", IdempotencyKey: key("con"), Lines: []ledger.Line{
				{LineNo: 1, AccountID: acct[e+dr], Side: ledger.Debit, Amount: money.MustNew(amount, money.Currency(ccy))},
				{LineNo: 2, AccountID: acct[e+cr], Side: ledger.Credit, Amount: money.MustNew(amount, money.Currency(ccy))}}})
	}
	oct := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	post("A", "ETB", oct, "1000", "3000", "1000000.00")
	post("A", "ETB", oct, "1400", "4100", "90000.00")
	post("B", "KES", oct, "1000", "3000", "500000.00")
	post("B", "KES", oct, "6400", "2400", "200000.00")
	post("B", "KES", time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC), "1000", "4000", "100000.00")
	post("C", "ETB", oct, "1000", "3000", "400000.00")

	ctrl, cfo := f.ctrl, f.cfo
	if _, err := f.svc.DesignateIntercompany(f.ctx, ctrl, acct["B2400"], ent["A"], "RECEIVABLE"); err == nil {
		t.Fatal("a liability was designated as an intercompany receivable")
	}
	for _, d := range []struct{ a, cp, role string }{{"A1400", "B", "RECEIVABLE"}, {"A4100", "B", "REVENUE"}, {"B2400", "A", "PAYABLE"}, {"B6400", "A", "EXPENSE"}} {
		_, err := f.svc.DesignateIntercompany(f.ctx, ctrl, acct[d.a], ent[d.cp], d.role)
		must(t, "designate "+d.a, err)
	}
	if _, err := f.svc.DesignateIntercompany(f.ctx, f.prep, acct["C1000"], ent["A"], "RECEIVABLE"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an accountant designated an intercompany account: %v", err)
	}

	members := []GroupMember{{EntityID: ent["A"], Ownership: dec("1"), Method: "FULL"}, {EntityID: ent["B"], Ownership: dec("0.8"), Method: "FULL"},
		{EntityID: ent["C"], Ownership: dec("0.25"), Method: "EQUITY"}}
	bad, err := f.svc.ProposeGroup(f.ctx, ctrl, GroupInput{Code: "serk", Name: "Serk Group", Presentation: "ETB", ParentID: ent["B"], Members: members})
	must(t, "group (wrong parent)", err)
	if err := f.svc.DecideGroup(f.ctx, cfo, bad, true, ""); err == nil {
		t.Fatal("a group whose parent is held at 80% was approved")
	}
	must(t, "reject", f.svc.DecideGroup(f.ctx, cfo, bad, false, "Parent must be Serk Ethiopia"))
	grp, err := f.svc.ProposeGroup(f.ctx, ctrl, GroupInput{Code: "SERK", Name: "Serk Group", Presentation: "ETB", ParentID: ent["A"], Members: members})
	must(t, "group", err)
	selfCFO := cfo
	selfCFO.UserID = ctrl.UserID
	if err := f.svc.DecideGroup(f.ctx, selfCFO, grp, true, ""); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("the proposer approved their own group: %v", err)
	}
	must(t, "approve group", f.svc.DecideGroup(f.ctx, cfo, grp, true, ""))

	in := ConsolidationRunInput{GroupID: grp, PeriodStart: "2026-10-01", PeriodEnd: "2026-12-31", RateSource: "NBE closing and Q4 average (test data)"}
	if _, err := f.svc.PrepareConsolidation(f.ctx, ctrl, in); err == nil {
		t.Fatal("a run without a KES rate was prepared")
	}
	in.Rates = map[string]RatePair{"KES": {Closing: dec("0.45"), Average: dec("0.44")}}
	run, err := f.svc.PrepareConsolidation(f.ctx, ctrl, in)
	must(t, "prepare", err)
	if _, err := f.svc.PrepareConsolidation(f.ctx, ctrl, in); err == nil {
		t.Fatal("a second live run for the same period")
	}
	rawFails(t, f, "forge a consolidation line", `INSERT INTO consolidation_lines (run_id, account_code, account_name, account_type, kind, local_amount, amount) VALUES ('`+run+`', 'X', 'X', 'ASSET', 'TB', 1, 1)`)

	rep, err := f.svc.ConsolidationReport(f.ctx, cfo, run)
	must(t, "report", err)
	tot := rep["totals"].(map[string]any)
	for k, want := range map[string]string{"assets": "1370000", "liabilities": "0", "equity": "1368000", "profit": "0", "suspense": "-2000"} {
		if got := tot[k].(interface{ String() string }).String(); !dec(got).Equal(dec(want)) {
			t.Errorf("%s: got %s, want %s", k, got, want)
		}
	}
	want := map[string]string{"1000": "1270000", "1400": "0", "2400": "0", "3000": "-1225000", "4100": "0", "6400": "0", "RE-BF": "-45000", "CTA": "2000",
		"NCI": "-36000", "NCI-EQ": "36000", "INV-ASSOC": "100000", "SHARE-ASSOC": "-100000", "IC-DIFF": "-2000"}
	tb := rep["trial_balance"].([]ConsolidatedRow)
	if len(tb) != len(want) {
		t.Errorf("consolidated rows: %d, want %d: %+v", len(tb), len(want), tb)
	}
	for _, r := range tb {
		if w, ok := want[r.Code]; !ok || !r.Total.Equal(dec(w)) {
			t.Errorf("%s %s: total %s, want %s", r.Code, r.Name, r.Total, w)
		}
	}
	if b := tb[0].ByEntity["SERK KENYA LTD"]; tb[0].Code != "1000" || !b.Equal(dec("270000")) {
		t.Errorf("Kenya cash translated: %s %s", tb[0].Code, b)
	}

	stale := cfo
	stale.AuthTime = time.Now().Add(-time.Hour)
	if err := f.svc.DecideConsolidation(f.ctx, stale, run, true, ""); err == nil {
		t.Fatal("a run was approved without fresh MFA")
	}
	if _, err := f.svc.ConsolidationView(f.ctx, f.prep); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an accountant read the consolidation: %v", err)
	}
	must(t, "cancel", f.svc.CancelConsolidation(f.ctx, ctrl, run, "Rates corrected by treasury"))
	in.Rates["KES"] = RatePair{Closing: dec("0.45"), Average: dec("0.45")}
	run2, err := f.svc.PrepareConsolidation(f.ctx, ctrl, in)
	must(t, "prepare again", err)
	rep, err = f.svc.ConsolidationReport(f.ctx, cfo, run2)
	must(t, "report 2", err)
	for _, r := range rep["trial_balance"].([]ConsolidatedRow) {
		if r.Code == "IC-DIFF" || r.Code == "CTA" {
			t.Errorf("at one rate nothing is unmatched and nothing is translated differently: %+v", r)
		}
	}
	must(t, "approve run", f.svc.DecideConsolidation(f.ctx, cfo, run2, true, ""))
	if err := f.svc.CancelConsolidation(f.ctx, ctrl, run2, "Too late to cancel"); err == nil {
		t.Fatal("an approved run was cancelled")
	}
	v, err := f.svc.ConsolidationView(f.ctx, ctrl)
	must(t, "view", err)
	if len(v["runs"].([]map[string]any)) != 2 || len(v["intercompany"].([]map[string]any)) != 4 || len(v["groups"].([]map[string]any)) != 2 {
		t.Fatalf("view: runs %d, intercompany %d, groups %d", len(v["runs"].([]map[string]any)), len(v["intercompany"].([]map[string]any)), len(v["groups"].([]map[string]any)))
	}
}
