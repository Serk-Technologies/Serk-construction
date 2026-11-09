package kernel

// Tests for workforce and payroll (WP-2.8, migration 0061, kernel/payroll.go).
//
// Golden fixture (hand-checked; the bands are tenant test data, not a
// statement of current law): monthly bands 0-600 0%, -1650 10% less 60,
// -3200 15% less 142.50, -5250 20% less 302.50, -7800 25% less 565,
// -10900 30% less 955, above 35% less 1,500. Pension 7% employee and
// 11% employer on basic. Overtime 1.5x; 208 hours a month, 8 a day.
//
//   E-001 salaried 20,000, 1 month, 10 h overtime, allowance 1,000 (project):
//     overtime 10 x 20,000/208 x 1.5 = 1,442.31; gross 22,442.31;
//     tax 22,442.31 x 35% - 1,500 = 6,354.81; pension 1,400 / 2,200; net 14,687.50
//   E-002 daily 400, 22 days (project): gross 8,800; tax 8,800 x 30% - 955 = 1,685;
//     pension 616 / 968; net 6,499
//   E-003 hourly 50, 160 h + 8 h overtime (head office): basic 8,000, overtime 600,
//     gross 8,600; tax 1,625; pension 560 / 880; net 6,415
//
//   Project section: salaries 31,242.31 + employer pension 3,168 = net 21,186.50
//     + tax 8,039.81 + pension payable 5,184.
//   Head office section: 8,600 + 880 = 6,415 + 1,625 + 1,440.
//   Payment: net pay 27,601.50 to payments in transit.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

func TestPayroll_RunApprovePay(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	now := time.Now()
	person := func(id string, role string) auth.Principal {
		return auth.Principal{UserID: id, TenantID: f.tenant, Roles: []string{role}, IssuedAt: now.Add(-time.Minute), Kind: auth.KindHuman, MFA: true, AuthTime: now}
	}
	officer, hr := person("u-payroll", "payroll_officer"), person("u-hr-manager", "hr_manager")
	must(t, "accounts", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, a := range []struct{ code, name, typ string }{{"6200", "Salaries and wages", "EXPENSE"}, {"6210", "Employer pension", "EXPENSE"},
			{"2250", "Net pay payable", "LIABILITY"}, {"2260", "Income tax payable", "LIABILITY"}, {"2310", "Pension payable", "LIABILITY"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: f.tenant, LegalEntityID: f.entity, Code: a.code, Name: a.name, Type: ledger.AccountType(a.typ), Active: true})
			if err != nil {
				return err
			}
			w.acct[a.code] = id
		}
		return nil
	}))
	journal := func(id string) string {
		var s string
		must(t, "journal", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT string_agg(a.code || ' ' || l.side || ' ' || l.amount::numeric(24,2), ', ' ORDER BY a.code, l.side) FROM journal_lines l JOIN accounts a ON a.id = l.account_id WHERE l.journal_id = $1::uuid`, id).Scan(&s)
		}))
		return s
	}

	// Policy: controller proposes, CFO approves.
	pol, err := f.svc.ProposePayrollPolicy(f.ctx, c.controller, PayrollPolicyInput{LegalEntityID: f.entity, Salary: w.acct["6200"], PensionExpense: w.acct["6210"],
		NetPayable: w.acct["2250"], TaxPayable: w.acct["2260"], PensionPayable: w.acct["2310"]})
	must(t, "policy", err)
	must(t, "policy approve", f.svc.DecidePayrollPolicy(f.ctx, fresh(c.cfo), pol, true, ""))

	// Rule set: officer prepares, HR manager approves.
	ptr := func(s string) *decimal.Decimal { d := dec(s); return &d }
	bands := []TaxBand{{UpTo: ptr("600"), Rate: dec("0")}, {UpTo: ptr("1650"), Rate: dec("0.10"), Deduction: dec("60")},
		{UpTo: ptr("3200"), Rate: dec("0.15"), Deduction: dec("142.50")}, {UpTo: ptr("5250"), Rate: dec("0.20"), Deduction: dec("302.50")},
		{UpTo: ptr("7800"), Rate: dec("0.25"), Deduction: dec("565")}, {UpTo: ptr("10900"), Rate: dec("0.30"), Deduction: dec("955")},
		{Rate: dec("0.35"), Deduction: dec("1500")}}
	rules := RuleSetInput{LegalEntityID: f.entity, EffectiveFrom: "2026-07-01", Bands: bands, PensionEE: dec("0.07"), PensionER: dec("0.11"), SourceRef: "Tenant payroll configuration 2026 (test data)"}
	if _, err := f.svc.CreateRuleSet(f.ctx, officer, RuleSetInput{LegalEntityID: f.entity, EffectiveFrom: "2026-07-01", Bands: []TaxBand{{UpTo: ptr("600"), Rate: dec("0.1")}, {UpTo: ptr("500"), Rate: dec("0.2")}, {Rate: dec("0.3")}},
		PensionEE: dec("0.07"), PensionER: dec("0.11"), SourceRef: "bad bands"}); err == nil {
		t.Fatal("descending tax bands were accepted")
	}
	rs, err := f.svc.CreateRuleSet(f.ctx, officer, rules)
	must(t, "rule set", err)
	must(t, "rule set submit", f.svc.SubmitRuleSet(f.ctx, officer, rs))
	if err := f.svc.DecideRuleSet(f.ctx, person("u-payroll", "hr_manager"), rs, true, ""); err == nil {
		t.Fatal("the preparer approved their own rule set")
	}
	must(t, "rule set approve", f.svc.DecideRuleSet(f.ctx, hr, rs, true, ""))

	// Employees.
	if _, err := f.svc.RegisterEmployee(f.ctx, c.pm, EmployeeInput{LegalEntityID: f.entity, Number: "E-009", Name: "Nobody", Kind: "DAILY", BaseRate: dec("1"), StartsOn: "2026-01-01"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager registered an employee: %v", err)
	}
	emp := map[string]string{}
	for _, e := range []EmployeeInput{
		{Number: "E-001", Name: "Abebe Kebede", Kind: "SALARIED", Trade: "Site engineer", BaseRate: dec("20000"), TIN: "0012345678", BankName: "CBE", AccountNumber: "1000111122223"},
		{Number: "E-002", Name: "Tigist Alemu", Kind: "DAILY", Trade: "Mason", BaseRate: dec("400"), BankName: "CBE", AccountNumber: "1000444455556"},
		{Number: "E-003", Name: "Dawit Haile", Kind: "HOURLY", Trade: "Electrician", BaseRate: dec("50")}} {
		e.LegalEntityID, e.StartsOn = f.entity, "2026-01-01"
		id, err := f.svc.RegisterEmployee(f.ctx, officer, e)
		must(t, "employee "+e.Number, err)
		emp[e.Number] = id
	}
	rawFails(t, f, "plain account number", `UPDATE employees SET bank_name = 'X' WHERE number = 'E-001'`)

	// October run.
	run, err := f.svc.CreatePayRun(f.ctx, officer, f.entity, "2026-10-01", "2026-10-31", "2026-10-31")
	must(t, "run", err)
	must(t, "inputs", f.svc.AddPayInputs(f.ctx, officer, run, []PayInput{
		{EmployeeID: emp["E-001"], Units: dec("1"), OvertimeHours: dec("10"), Allowances: dec("1000"), ProjectID: f.project, CostCodeID: w.costCode},
		{EmployeeID: emp["E-002"], Units: dec("22"), ProjectID: f.project, CostCodeID: w.costCode},
		{EmployeeID: emp["E-003"], Units: dec("160"), OvertimeHours: dec("8")}}))
	if err := f.svc.AddPayInputs(f.ctx, officer, run, []PayInput{{EmployeeID: emp["E-001"], Units: dec("2")}}); err == nil {
		t.Fatal("a second input (two months) for a salaried employee")
	}
	rawFails(t, f, "forge a pay line", `INSERT INTO pay_lines (run_id, employee_id, basic, overtime, allowances, gross, income_tax, pension_ee, pension_er, net) VALUES ('`+run+`', '`+emp["E-003"]+`', 9000, 0, 0, 9000, 0, 0, 0, 9000)`)
	must(t, "calculate", f.svc.CalculatePayRun(f.ctx, officer, run))
	view, err := f.svc.PayrollView(f.ctx, hr, run)
	must(t, "view", err)
	lines := view["lines"].([]map[string]any)
	got := []string{}
	for _, l := range lines {
		got = append(got, l["number"].(string)+" "+dec(fmt.Sprint(l["gross"])).StringFixed(2)+" "+dec(fmt.Sprint(l["income_tax"])).StringFixed(2)+" "+dec(fmt.Sprint(l["net"])).StringFixed(2))
	}
	if strings.Join(got, "; ") != "E-001 22442.31 6354.81 14687.50; E-002 8800.00 1685.00 6499.00; E-003 8600.00 1625.00 6415.00" {
		t.Fatalf("golden lines: %s", strings.Join(got, "; "))
	}
	for _, e := range view["employees"].([]map[string]any) {
		if a, ok := e["account"].(string); ok && !strings.HasPrefix(a, "****") {
			t.Fatalf("an account number is shown unmasked: %v", e)
		}
	}

	// Submit, approve (not the preparer, fresh MFA), pay (a third person).
	if err := f.svc.DecidePayRun(f.ctx, hr, run, true, ""); err == nil {
		t.Fatal("a calculated (unsubmitted) run was approved")
	}
	must(t, "submit", f.svc.SubmitPayRun(f.ctx, officer, run))
	if err := f.svc.DecidePayRun(f.ctx, person("u-payroll", "hr_manager"), run, true, ""); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("the preparer approved their own run: %v", err)
	}
	stale := hr
	stale.AuthTime = now.Add(-time.Hour)
	if err := f.svc.DecidePayRun(f.ctx, stale, run, true, ""); err == nil {
		t.Fatal("a run was approved without fresh MFA")
	}
	must(t, "approve", f.svc.DecidePayRun(f.ctx, hr, run, true, ""))
	sections := map[string]string{}
	must(t, "sections", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT COALESCE(project_id::text, 'HQ'), journal_id::text FROM pay_run_sections WHERE run_id = $1::uuid`, run)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var k, j string
			if err := rows.Scan(&k, &j); err != nil {
				return err
			}
			sections[k] = j
		}
		return rows.Err()
	}))
	if len(sections) != 2 {
		t.Fatalf("sections: %v", sections)
	}
	if got := journal(sections[f.project]); got != "2250 CREDIT 21186.50, 2260 CREDIT 8039.81, 2310 CREDIT 5184.00, 6200 DEBIT 31242.31, 6210 DEBIT 3168.00" {
		t.Fatalf("project payroll journal: %s", got)
	}
	if got := journal(sections["HQ"]); got != "2250 CREDIT 6415.00, 2260 CREDIT 1625.00, 2310 CREDIT 1440.00, 6200 DEBIT 8600.00, 6210 DEBIT 880.00" {
		t.Fatalf("head office payroll journal: %s", got)
	}
	w.financePost(t, sections[f.project], sections["HQ"])

	// A rule set dated into the approved period is refused; the next month is fine.
	if _, err := f.svc.CreateRuleSet(f.ctx, officer, RuleSetInput{LegalEntityID: f.entity, EffectiveFrom: "2026-10-15", Bands: bands, PensionEE: dec("0.07"), PensionER: dec("0.11"), SourceRef: "Retro change"}); err == nil {
		t.Fatal("a rule set was retro-dated into an approved pay run")
	}
	_, err = f.svc.CreateRuleSet(f.ctx, officer, RuleSetInput{LegalEntityID: f.entity, EffectiveFrom: "2026-11-01", Bands: bands, PensionEE: dec("0.07"), PensionER: dec("0.11"), SourceRef: "November change"})
	must(t, "november rule set", err)

	if _, err := f.svc.ReleasePayRun(f.ctx, hr, run, w.bank); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the HR manager released the payment: %v", err)
	}
	pj, err := f.svc.ReleasePayRun(f.ctx, fresh(c.treasury), run, w.bank)
	must(t, "release", err)
	if got := journal(pj); got != "1050 CREDIT 27601.50, 2250 DEBIT 27601.50" {
		t.Fatalf("payment journal: %s", got)
	}
	w.financePost(t, pj)
	file, err := f.svc.PayrollBankFile(f.ctx, fresh(c.treasury), run)
	must(t, "bank file", err)
	if !strings.Contains(string(file), "E-001,Abebe Kebede,CBE,1000111122223,14687.50") || !strings.Contains(string(file), "E-003,Dawit Haile,,,6415.00") {
		t.Fatalf("bank file:\n%s", file)
	}
	if _, err := f.svc.PayrollBankFile(f.ctx, hr, run); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the HR manager exported account numbers: %v", err)
	}
	if _, err := f.svc.PayrollView(f.ctx, c.pm, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager read the payroll: %v", err)
	}

	// A draft run is cancelled with a reason; a leaver cannot be paid after leaving.
	must(t, "leaver", f.svc.EmployeeLeaves(f.ctx, officer, emp["E-002"], "2026-10-31"))
	nov, err := f.svc.CreatePayRun(f.ctx, officer, f.entity, "2026-11-01", "2026-11-30", "2026-11-30")
	must(t, "november run", err)
	if err := f.svc.AddPayInputs(f.ctx, officer, nov, []PayInput{{EmployeeID: emp["E-002"], Units: dec("5")}}); err == nil {
		t.Fatal("a leaver was paid after leaving")
	}
	if err := f.svc.CancelPayRun(f.ctx, officer, nov, "x"); err == nil {
		t.Fatal("a run was cancelled without a reason")
	}
	must(t, "cancel", f.svc.CancelPayRun(f.ctx, officer, nov, "No November payroll in this test"))
}
