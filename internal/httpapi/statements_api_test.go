package httpapi

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

// TestAPIFinancialStatements (CAP-01.5). Hand-computed reference, independent
// of the code under test. Posted journals:
//
//	01 Sep  capital      Dr cash 10,000   Cr equity 10,000
//	20 Sep  expense      Dr expense 200   Cr cash 200          (before the period)
//	10 Oct  sale         Dr cash 4,000    Cr revenue 4,000
//	15 Oct  expense      Dr expense 1,500 Cr cash 1,500
//	31 Oct  accrual      Dr expense 300   Cr liability 300
//	12 Oct  a draft      Dr expense 999   Cr cash 999          (never posted: excluded)
//
// October: revenue 4,000, expenses 1,800, profit 2,200. At 31 October: cash
// 12,300 = liability 300 + equity 10,000 + unclosed earnings 2,000.
func TestAPIFinancialStatements(t *testing.T) {
	e := newEnv(t)
	err := e.db.AdminStore.WithTenant(e.db.Ctx, e.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, a := range []struct{ code, typ string }{{"2000", "LIABILITY"}, {"3000", "EQUITY"}, {"4000", "REVENUE"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: e.tenant, LegalEntityID: e.entity, Code: a.code, Name: a.code, Type: ledger.AccountType(a.typ), Active: true})
			if err != nil {
				return err
			}
			e.accounts[a.code] = id
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prep, appr, ctrl := e.token("prep", "accountant"), e.token("appr", "finance_manager"), e.token("ctrl", "controller")
	journal := func(date, dr, cr, amount string, post bool) {
		t.Helper()
		body := map[string]any{"legal_entity_id": e.entity, "functional_currency": "ETB", "business_event_at": date + "T09:00:00Z",
			"description": "statements test", "source_type": "MANUAL", "idempotency_key": "fs-" + uuid.NewString(),
			"lines": []map[string]any{
				{"account_id": e.accounts[dr], "side": "DEBIT", "amount": map[string]string{"amount": amount, "currency": "ETB"}},
				{"account_id": e.accounts[cr], "side": "CREDIT", "amount": map[string]string{"amount": amount, "currency": "ETB"}}}}
		st, out := e.do("POST", "/v1/journals", prep, body)
		if st != 201 {
			t.Fatalf("journal: %d %v", st, out)
		}
		if !post {
			return
		}
		id := out["id"].(string)
		if st, out := e.do("POST", "/v1/journals/"+id+"/approve", appr, nil); st != 200 {
			t.Fatalf("approve: %d %v", st, out)
		}
		if st, out := e.do("POST", "/v1/journals/"+id+"/post", ctrl, nil); st != 200 {
			t.Fatalf("post: %d %v", st, out)
		}
	}
	journal("2026-09-01", "1000", "3000", "10000.00", true)
	journal("2026-09-20", "5000", "1000", "200.00", true)
	journal("2026-10-10", "1000", "4000", "4000.00", true)
	journal("2026-10-15", "5000", "1000", "1500.00", true)
	journal("2026-10-31", "5000", "2000", "300.00", true)
	journal("2026-10-12", "5000", "1000", "999.00", false)

	url := "/v1/reports/financial-statements?entity=" + e.entity + "&from=2026-10-01&to=2026-10-31"
	if st, _ := e.do("GET", url, e.token("u-site", "site_engineer"), nil); st != 403 {
		t.Fatalf("a site engineer read the statements: %d", st)
	}
	st, fs := e.do("GET", url, appr, nil)
	if st != 200 {
		t.Fatalf("statements: %d %v", st, fs)
	}
	want := map[string]string{"total_revenue": "4000", "total_expenses": "1800", "net_profit": "2200", "total_assets": "12300",
		"total_liabilities": "300", "total_equity": "10000", "unclosed_earnings": "2000", "difference": "0"}
	for k, v := range want {
		if got, _ := fs[k].(string); got != v {
			t.Errorf("%s = %v, want %s", k, fs[k], v)
		}
	}
	if fs["balanced"] != true {
		t.Errorf("the balance sheet does not balance: %v", fs)
	}
	assets, _ := fs["assets"].([]any)
	if len(assets) != 1 || assets[0].(map[string]any)["account_code"] != "1000" || assets[0].(map[string]any)["amount"] != "12300" {
		t.Errorf("assets: %v", fs["assets"])
	}
	if st, _ := e.do("GET", "/v1/reports/financial-statements?entity="+e.entity+"&from=2026-10-31&to=2026-10-01", appr, nil); st != 422 {
		t.Errorf("a period ending before it starts: %d", st)
	}
}
