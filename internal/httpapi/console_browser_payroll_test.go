package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

// TestConsoleBrowserPayroll drives a pay run (migration 0061) through the web
// console in a real Chromium: the payroll officer opens the run, enters the
// inputs, calculates and submits; the HR manager approves with fresh MFA;
// treasury releases the payment. Accounts, rules and employees are set up
// through the API. Skipped when node or playwright is not installed.
func TestConsoleBrowserPayroll(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: the browser journey was not run")
	}
	pw := os.Getenv("SERK_PLAYWRIGHT")
	if pw == "" {
		if out, err := exec.Command("npm", "root", "-g").Output(); err == nil {
			pw = filepath.Join(strings.TrimSpace(string(out)), "playwright")
		}
	}
	if _, err := os.Stat(filepath.Join(pw, "package.json")); err != nil {
		t.Skipf("playwright module not found at %q: the browser journey was not run", pw)
	}
	script, err := filepath.Abs("../../e2e/console_payroll.mjs")
	if err != nil {
		t.Fatal(err)
	}
	a := newP2PAPI(t)
	if err := a.db.AdminStore.WithTenant(a.db.Ctx, a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, x := range []struct{ code, typ string }{{"6200", "EXPENSE"}, {"6210", "EXPENSE"}, {"2250", "LIABILITY"}, {"2260", "LIABILITY"}, {"2310", "LIABILITY"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: a.tenant, LegalEntityID: a.entity, Code: x.code, Name: x.code, Type: ledger.AccountType(x.typ), Active: true})
			if err != nil {
				return err
			}
			a.accounts[x.code] = id
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := New(Config{Verifier: testVerifier, Kernel: a.svc, Store: a.db.Router, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		RatePerMinute: 100000, Abuse: AbuseConfig{PreAuthPerMinute: 1000000, MaxInFlight: 1024}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	a.srv = srv
	cfo := a.tok("u-cfo-seed", nil, true, "cfo")
	pol := a.id("POST", "/v1/payroll-policies", a.tok("u-controller-seed", nil, false, "controller"), map[string]any{"legal_entity_id": a.entity,
		"salary_account_id": a.accounts["6200"], "pension_expense_account_id": a.accounts["6210"], "net_payable_account_id": a.accounts["2250"],
		"tax_payable_account_id": a.accounts["2260"], "pension_payable_account_id": a.accounts["2310"]})
	a.call(200, "POST", "/v1/payroll-policies/"+pol+"/approve", cfo, nil)
	officer := a.tok("u-payroll", nil, true, "payroll_officer")
	rs := a.id("POST", "/v1/payroll-rule-sets", officer, map[string]any{"legal_entity_id": a.entity, "effective_from": "2026-01-01", "pension_ee_rate": "0.07", "pension_er_rate": "0.11",
		"source_ref": "Tenant payroll configuration (test data)", "tax_bands": []map[string]any{{"upto": 600, "rate": 0, "deduction": 0}, {"upto": 10900, "rate": 0.30, "deduction": 955}, {"upto": nil, "rate": 0.35, "deduction": 1500}}})
	a.call(200, "POST", "/v1/payroll-rule-sets/"+rs+"/submit", officer, nil)
	hrSeed := a.tok("u-hr-seed", nil, true, "hr_manager")
	a.call(200, "POST", "/v1/payroll-rule-sets/"+rs+"/approve", hrSeed, nil)
	for _, e := range []map[string]any{{"number": "E-001", "name": "Abebe Kebede", "kind": "SALARIED", "base_rate": "20000", "bank_name": "CBE", "account_number": "1000111122223"},
		{"number": "E-002", "name": "Tigist Alemu", "kind": "DAILY", "base_rate": "400", "bank_name": "CBE", "account_number": "1000444455556"}} {
		e["legal_entity_id"], e["starts_on"] = a.entity, "2026-01-01"
		a.id("POST", "/v1/employees", officer, e)
	}
	bank := a.id("POST", "/v1/company-bank-accounts", a.tok("u-treasury-seed", nil, true, "treasury_officer"), map[string]any{"legal_entity_id": a.entity,
		"name": "CBE Payroll", "account_number": "1000999888777", "currency": "ETB", "gl_account_id": a.accounts["1000"], "clearing_account_id": a.accounts["1050"]})
	a.call(200, "POST", "/v1/company-bank-accounts/"+bank+"/approve", cfo, nil)

	tokens := map[string]string{"officer": officer, "hr": a.tok("u-hr-manager", nil, true, "hr_manager"), "treasury": a.tok("u-treasury", nil, true, "treasury_officer"),
		"hrPhone": a.tok("u-hr-manager", nil, true, "hr_manager")}
	dir := t.TempDir()
	fx, _ := json.Marshal(map[string]any{"tokens": tokens, "entity": a.entity, "project": a.project})
	fixture := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(fixture, fx, 0o600); err != nil {
		t.Fatal(err)
	}
	shots := os.Getenv("SERK_E2E_SHOTS_PAYROLL")
	if shots == "" {
		shots = filepath.Join(dir, "shots")
	}
	if err := os.MkdirAll(shots, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script)
	cmd.Env = append(os.Environ(), "SERK_URL="+srv.URL, "SERK_FIXTURE="+fixture, "SERK_SHOTS="+shots, "SERK_PLAYWRIGHT="+pw)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	t.Logf("browser payroll journey (%s):\n%s", time.Since(start).Round(time.Second), out)
	if err != nil {
		t.Fatalf("browser payroll journey failed: %v (screenshots in %s)", err, shots)
	}
	v := a.call(200, "GET", "/v1/payroll", hrSeed, nil)
	runs := v["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["status"] != "PAID" {
		t.Errorf("runs after the UI journey: %v", runs)
	}
}
