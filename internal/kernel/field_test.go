package kernel

// Tests for timesheets, equipment and HSE (WP-3.3, WP-3.4, WP-3.7, migration 0064, kernel/field.go).
// Fixture (hand-checked): excavator hire rate 800 an hour; one day 8 hours working, 2 idle -> charge 6,400;
// fuel 200 litres / 8 working hours = 25 l/h against a norm of 18 (anomaly above 21.6).
// Hours: 8 normal + 2 overtime for the hourly person; the daily-rate person works 1 day.
// LTIFR = 1 lost-time injury x 1,000,000 / 18 approved hours (8 + 2 overtime + 8) = 55,555.56.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/serk-group/serk-construction/internal/store/postgres"
	"github.com/serk-group/serk-construction/pkg/auth"
	"github.com/serk-group/serk-construction/pkg/ledger"
)

func TestField_TimesheetsEquipmentHSE(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	now := time.Now()
	person := func(id, role string, projects []string) auth.Principal {
		return auth.Principal{UserID: id, TenantID: f.tenant, Roles: []string{role}, ProjectIDs: projects, IssuedAt: now.Add(-time.Minute), Kind: auth.KindHuman}
	}
	proj := []string{f.project}
	eng, eng2 := c.siteEng, person("u-site-engineer-2", "site_engineer", proj)
	pm := c.pm
	hse, hse2 := person("u-hse", "hse_officer", nil), person("u-hse-2", "hse_officer", nil)
	plant := person("u-plant", "plant_manager", nil)
	payroll := person("u-payroll", "payroll_officer", nil)
	day := time.Now().UTC().AddDate(0, 0, -1)
	dayS := day.Format("2006-01-02")

	// People (payroll officer registers; the site sees names only).
	emp := map[string]string{}
	for _, e := range []EmployeeInput{{Number: "E-001", Name: "Dawit Haile", Kind: "HOURLY", BaseRate: dec("50")}, {Number: "E-002", Name: "Tigist Alemu", Kind: "DAILY", BaseRate: dec("400")},
		{Number: "E-003", Name: "Abebe Kebede", Kind: "SALARIED", BaseRate: dec("20000")}} {
		e.LegalEntityID, e.StartsOn = f.entity, "2026-01-01"
		id, err := f.svc.RegisterEmployee(f.ctx, payroll, e)
		must(t, "employee", err)
		emp[e.Number] = id
	}
	roster, err := f.svc.Roster(f.ctx, eng, f.project)
	must(t, "roster", err)
	if len(roster) != 3 {
		t.Fatalf("roster: %v", roster)
	}
	for _, r := range roster {
		if _, ok := r["base_rate"]; ok {
			t.Fatalf("the site roster shows pay data: %v", r)
		}
	}

	// ---- timesheet: the engineer records, the project manager approves.
	ts := TimesheetIn{ProjectID: f.project, WorkDate: dayS, Lines: []TimesheetLineIn{{EmployeeID: emp["E-001"], CostCodeID: w.costCode, HoursNormal: dec("8"), HoursOT: dec("2")}, {EmployeeID: emp["E-002"], HoursNormal: dec("8")}}}
	if _, err := f.svc.CreateTimesheet(f.ctx, pm, ts); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager recorded a timesheet: %v", err)
	}
	if _, err := f.svc.CreateTimesheet(f.ctx, eng, TimesheetIn{ProjectID: f.project, WorkDate: dayS, Lines: []TimesheetLineIn{{EmployeeID: emp["E-001"], HoursNormal: dec("10"), HoursOT: dec("7")}}}); err == nil {
		t.Fatal("17 hours in a day were accepted")
	}
	sheet, err := f.svc.CreateTimesheet(f.ctx, eng, ts)
	must(t, "timesheet", err)
	// The induction gate: nobody is submitted without a valid induction.
	if err := f.svc.SetInductionGate(f.ctx, eng, true); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an engineer set the induction gate: %v", err)
	}
	must(t, "gate on", f.svc.SetInductionGate(f.ctx, hse, true))
	if err := f.svc.SubmitTimesheet(f.ctx, eng, sheet); err == nil {
		t.Fatal("a timesheet was submitted without inductions while the gate is on")
	}
	for _, e := range []string{"E-001", "E-002"} {
		must(t, "induction", f.svc.RecordInduction(f.ctx, hse, emp[e], f.project, "2026-01-05", "2027-01-04", "Site rules, PPE"))
	}
	must(t, "submit", f.svc.SubmitTimesheet(f.ctx, eng, sheet))
	if err := f.svc.DecideTimesheet(f.ctx, eng2, sheet, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an engineer approved a timesheet: %v", err)
	}
	must(t, "approve", f.svc.DecideTimesheet(f.ctx, pm, sheet, true, ""))
	dup, err := f.svc.CreateTimesheet(f.ctx, eng2, TimesheetIn{ProjectID: f.project, WorkDate: dayS, Lines: []TimesheetLineIn{{EmployeeID: emp["E-001"], HoursNormal: dec("4")}}})
	must(t, "second timesheet", err)
	must(t, "submit second", f.svc.SubmitTimesheet(f.ctx, eng2, dup))
	if err := f.svc.DecideTimesheet(f.ctx, pm, dup, true, ""); err == nil {
		t.Fatal("a second approved line for the same person, project and day")
	}
	must(t, "reject second", f.svc.DecideTimesheet(f.ctx, pm, dup, false, "Hours already booked on the first sheet"))
	rawFails(t, f, "edit an approved timesheet line", `UPDATE timesheet_lines SET hours_normal = 1`)
	d, err := f.svc.TimesheetDetail(f.ctx, pm, sheet)
	must(t, "detail", err)
	if len(d["lines"].([]map[string]any)) != 2 {
		t.Fatalf("detail: %v", d)
	}

	// ---- payroll import: the daily person 1 day, the hourly person 8 hours, overtime from the sheet.
	must(t, "payroll accounts", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, a := range []struct{ code, name, typ string }{{"2150", "Pension payable", "LIABILITY"}, {"2160", "Tax payable", "LIABILITY"}, {"2170", "Net pay payable", "LIABILITY"}, {"6300", "Salaries", "EXPENSE"}, {"6310", "Employer pension", "EXPENSE"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: f.tenant, LegalEntityID: f.entity, Code: a.code, Name: a.name, Type: ledger.AccountType(a.typ), Active: true})
			if err != nil {
				return err
			}
			w.acct[a.code] = id
		}
		return nil
	}))
	pol, err := f.svc.ProposePayrollPolicy(f.ctx, c.controller, PayrollPolicyInput{LegalEntityID: f.entity, Salary: w.acct["6300"], PensionExpense: w.acct["6310"], NetPayable: w.acct["2170"], TaxPayable: w.acct["2160"], PensionPayable: w.acct["2150"]})
	must(t, "payroll policy", err)
	must(t, "payroll policy approve", f.svc.DecidePayrollPolicy(f.ctx, fresh(c.cfo), pol, true, ""))
	hrm := person("u-hr-manager", "hr_manager", nil)
	hrm.MFA, hrm.AuthTime = true, now
	z := dec("0")
	rs, err := f.svc.CreateRuleSet(f.ctx, payroll, RuleSetInput{LegalEntityID: f.entity, EffectiveFrom: "2026-01-01", Bands: []TaxBand{{Rate: dec("0.1"), Deduction: z}}, PensionEE: dec("0.07"), PensionER: dec("0.11"), SourceRef: "Test rules"})
	must(t, "rule set", err)
	must(t, "rule submit", f.svc.SubmitRuleSet(f.ctx, payroll, rs))
	must(t, "rule approve", f.svc.DecideRuleSet(f.ctx, hrm, rs, true, ""))
	monthStart := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, -1)
	prun, err := f.svc.CreatePayRun(f.ctx, payroll, f.entity, monthStart.Format("2006-01-02"), monthEnd.Format("2006-01-02"), monthEnd.Format("2006-01-02"))
	must(t, "pay run", err)
	n, err := f.svc.ImportTimesheetsToPayRun(f.ctx, payroll, prun)
	must(t, "import", err)
	if n != 2 {
		t.Fatalf("imported %d people, want 2 (the salaried person has no timesheet)", n)
	}
	var units, ot string
	must(t, "inputs", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT string_agg(e.number || '=' || i.units::numeric(12,2) || '/' || i.overtime_hours::numeric(10,2), ',' ORDER BY e.number), '' FROM pay_inputs i JOIN employees e ON e.id = i.employee_id WHERE i.run_id = $1::uuid`, prun).Scan(&units, &ot)
	}))
	if units != "E-001=8.00/2.00,E-002=1.00/0.00" {
		t.Fatalf("pay inputs: %s", units)
	}
	if n2, _ := f.svc.ImportTimesheetsToPayRun(f.ctx, payroll, prun); n2 != 0 {
		t.Fatalf("a second import added %d people", n2)
	}

	// ---- equipment.
	ex, err := f.svc.RegisterEquipment(f.ctx, plant, EquipmentIn{LegalEntityID: f.entity, Code: "exc-01", Name: "Excavator CAT 320", Class: "Excavator", Ownership: "OWNED", HireRate: dec("800"), RateUnit: "HOUR", FuelNormLPH: dec("18"), ServiceIntervalHours: dec("100")})
	must(t, "equipment", err)
	if _, err := f.svc.RegisterEquipment(f.ctx, eng, EquipmentIn{LegalEntityID: f.entity, Code: "X", Name: "x", Class: "x", Ownership: "OWNED", HireRate: dec("1"), RateUnit: "HOUR"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an engineer registered equipment: %v", err)
	}
	use, err := f.svc.RecordUsage(f.ctx, eng, UsageIn{EquipmentID: ex, ProjectID: f.project, CostCodeID: w.costCode, WorkDate: dayS, HoursWork: dec("8"), HoursIdle: dec("2"), LitresFuel: dec("200"), Operator: "Girma"})
	must(t, "usage", err)
	if _, err := f.svc.RecordUsage(f.ctx, eng2, UsageIn{EquipmentID: ex, ProjectID: f.project, WorkDate: dayS, HoursWork: dec("1")}); err == nil {
		t.Fatal("a second usage record for the same equipment, project and day")
	}
	must(t, "submit usage", f.svc.SubmitUsage(f.ctx, eng, use))
	if err := f.svc.DecideUsage(f.ctx, eng2, use, true, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an engineer approved usage: %v", err)
	}
	must(t, "approve usage", f.svc.DecideUsage(f.ctx, pm, use, true, ""))
	var charge string
	must(t, "charge", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT charge::text FROM equipment_usage WHERE id = $1::uuid`, use).Scan(&charge)
	}))
	if !dec(charge).Equal(dec("6400")) {
		t.Fatalf("charge %s, want 6400", charge)
	}
	rawFails(t, f, "change a charge", `UPDATE equipment_usage SET charge = 1 WHERE id = '`+use+`'`)
	ev, err := f.svc.EquipmentView(f.ctx, plant)
	must(t, "equipment view", err)
	row := ev["equipment"].([]map[string]any)[0]
	if row["fuel_anomaly"] != true || row["service_due"] != false {
		t.Fatalf("equipment row: %v", row)
	}
	// Hire charge: one draft journal for the project, Dr cost 6,400 / Cr recovery 6,400.
	if _, _, err := f.svc.PrepareHireRun(f.ctx, plant, f.entity, dayS, dayS, w.acct["5000"], w.acct["5100"]); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("plant manager prepared the hire run: %v", err)
	}
	run, jids, err := f.svc.PrepareHireRun(f.ctx, f.prep, f.entity, dayS, dayS, w.acct["5000"], w.acct["5100"])
	must(t, "hire run", err)
	if len(jids) != 1 {
		t.Fatalf("journals: %v", jids)
	}
	var lines string
	must(t, "journal", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT string_agg(a.code || ' ' || l.side || ' ' || l.amount::numeric(24,2), ', ' ORDER BY a.code) FROM journal_lines l JOIN accounts a ON a.id = l.account_id WHERE l.journal_id = $1::uuid`, jids[0]).Scan(&lines)
	}))
	if lines != "5000 DEBIT 6400.00, 5100 CREDIT 6400.00" {
		t.Fatalf("hire journal: %s (run %s)", lines, run)
	}
	if _, _, err := f.svc.PrepareHireRun(f.ctx, f.prep, f.entity, dayS, dayS, w.acct["5000"], w.acct["5100"]); err == nil {
		t.Fatal("usage was charged twice")
	}
	wo, err := f.svc.OpenWorkOrder(f.ctx, plant, WorkOrderIn{EquipmentID: ex, Kind: "service", Description: "250 hour service"})
	must(t, "work order", err)
	must(t, "complete", f.svc.CompleteWorkOrder(f.ctx, plant, wo, dec("18500")))
	rawFails(t, f, "reopen a work order", `UPDATE equipment_work_orders SET status = 'OPEN' WHERE id = '`+wo+`'`)

	// ---- HSE.
	if _, err := f.svc.ReportIncident(f.ctx, eng, IncidentIn{ProjectID: f.project, Kind: "LOST_TIME", Severity: "MAJOR", OccurredAt: now.Add(-3 * time.Hour).Format(time.RFC3339), Description: "Fall from scaffold", PeopleInvolved: 1, LostDays: 0}); err == nil {
		t.Fatal("a lost-time injury with no lost days")
	}
	inc, err := f.svc.ReportIncident(f.ctx, eng, IncidentIn{ProjectID: f.project, Kind: "LOST_TIME", Severity: "MAJOR", OccurredAt: now.Add(-3 * time.Hour).Format(time.RFC3339), Location: "Block A", Description: "Fall from scaffold, wrist fracture", PeopleInvolved: 1, LostDays: 3})
	must(t, "incident", err)
	var notified int
	must(t, "notified", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'hse.incident' AND subject_id = $1`, inc).Scan(&notified)
	}))
	if notified != 2 {
		t.Fatalf("a major incident notified %d recipients, want 2", notified)
	}
	if err := f.svc.CloseIncident(f.ctx, hse, inc, "Closed too early"); err == nil {
		t.Fatal("an incident was closed before its investigation")
	}
	must(t, "investigate", f.svc.InvestigateIncident(f.ctx, hse, inc, "Missing toe boards on the scaffold", "Scaffold was handed over without inspection"))
	if err := f.svc.CloseIncident(f.ctx, hse, inc, "Closed with no action"); err == nil {
		t.Fatal("a major incident was closed without a corrective action")
	}
	capa, err := f.svc.RaiseCAPA(f.ctx, hse, inc, "Inspect every scaffold before handover", "Site engineer", time.Now().AddDate(0, 0, 14).Format("2006-01-02"))
	must(t, "capa", err)
	must(t, "capa done", f.svc.CompleteCAPA(f.ctx, hse, capa))
	if err := f.svc.VerifyCAPA(f.ctx, hse, capa); err == nil {
		t.Fatal("the person who completed an action verified it")
	}
	must(t, "capa verify", f.svc.VerifyCAPA(f.ctx, hse2, capa))
	reporter := hse
	reporter.UserID = eng.UserID
	if err := f.svc.CloseIncident(f.ctx, reporter, inc, "Closed by the reporter"); err == nil {
		t.Fatal("the reporter closed their own incident")
	}
	must(t, "close", f.svc.CloseIncident(f.ctx, hse2, inc, "Investigated, action verified"))

	// Permit to work: requester, issuer and acceptor are three people.
	from, to := now.Add(-time.Hour).Format(time.RFC3339), now.Add(8*time.Hour).Format(time.RFC3339)
	ptw, err := f.svc.RequestPermit(f.ctx, eng, PermitIn{ProjectID: f.project, Kind: "hot_work", Location: "Block A, level 3", Description: "Welding of balustrade", ValidFrom: from, ValidTo: to})
	must(t, "permit", err)
	if err := f.svc.IssuePermit(f.ctx, eng, ptw, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an engineer issued a permit: %v", err)
	}
	must(t, "issue", f.svc.IssuePermit(f.ctx, hse, ptw, "Fire watch for 30 minutes after work"))
	if err := f.svc.AcceptPermit(f.ctx, hse, ptw); err == nil {
		t.Fatal("the issuer accepted their own permit")
	}
	must(t, "accept", f.svc.AcceptPermit(f.ctx, eng2, ptw))
	must(t, "close permit", f.svc.ClosePermit(f.ctx, eng2, ptw))
	if _, err := f.svc.RequestPermit(f.ctx, eng, PermitIn{ProjectID: f.project, Kind: "excavation", Location: "Trench", Description: "Long trench permit", ValidFrom: from, ValidTo: now.Add(40 * 24 * time.Hour).Format(time.RFC3339)}); err == nil {
		t.Fatal("a permit valid for 40 days")
	}
	must(t, "toolbox", f.svc.RecordToolboxTalk(f.ctx, hse, f.project, dayS, "Working at height", 14, ""))
	must(t, "inspection", f.svc.RecordSafetyInspection(f.ctx, hse, f.project, dayS, "Scaffolding block A", 72, "Toe boards missing on level 3"))

	hv, err := f.svc.HSEView(f.ctx, hse)
	must(t, "hse view", err)
	k := hv["kpi"].(map[string]any)
	if k["lost_time_injuries_12m"] != 1 || !k["hours_worked_12m"].(interface{ String() string }).(interface{ IsPositive() bool }).IsPositive() {
		t.Fatalf("kpi: %v", k)
	}
	if got := k["ltifr"].(interface{ String() string }).String(); !strings.HasPrefix(got, "55555.56") {
		t.Fatalf("LTIFR %s, want 55555.56 (1 injury per 18 hours)", got)
	}
	if _, err := f.svc.HSEView(f.ctx, w.cast.buyer); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a buyer read the HSE register: %v", err)
	}
	_ = postgres.ErrUnknownDocType
}
