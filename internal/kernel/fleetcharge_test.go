package kernel

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

// TestFleet_ChargeOut (FLT-01.14, 0079). Hand-checked figures, independent of
// the code under test: machine A works 6.5 h and idles 1.5 h at 800 / 200 an
// hour = 5,200.00 + 300.00 = 5,500.00; machine B works 8 h at 1,000 = 8,000.00;
// the month charges 13,500.00: Dr equipment cost on the project, Cr recovery.
func TestFleet_ChargeOut(t *testing.T) {
	w := newFleetWorld(t)
	f, c := w.f, w.cast
	acct := map[string]string{}
	must(t, "accounts", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, a := range []struct{ code, name, typ string }{{"6400", "Equipment cost", "EXPENSE"}, {"6490", "Equipment recovery", "EXPENSE"}, {"1500", "Plant at cost", "ASSET"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: f.tenant, LegalEntityID: f.entity, Code: a.code, Name: a.name, Type: ledger.AccountType(a.typ), Active: true})
			if err != nil {
				return err
			}
			acct[a.code] = id
		}
		return nil
	}))
	working := func(serial string) string {
		eq := w.machine(t, serial)
		w.reserve(t, eq, f.project, w.eng, 0, 30)
		for _, step := range [][2]string{{"MOBILIZING", f.project}, {"ASSIGNED", ""}, {"OPERATING", ""}} {
			by := w.eng
			if step[0] == "MOBILIZING" {
				by = w.officer
			}
			if _, err := f.svc.MoveEquipment(f.ctx, by, eq, step[0], "Start of work", step[1]); err != nil {
				t.Fatalf("%s: %v", step[0], err)
			}
		}
		return eq
	}
	w.authorise(t, "op-abebe")
	day := today().Format("2006-01-02")
	month := today().Format("2006-01")
	logOf := func(eq, prod, idle, open, close string) {
		id, err := f.svc.CreateEquipmentLog(f.ctx, w.eng, EquipmentLogInput{EquipmentID: eq, ProjectID: f.project, LogDate: day, Operator: "op-abebe",
			MeterOpen: dec(open), MeterClose: dec(close), ProductiveHours: dec(prod), IdleHours: dec(idle), CostCodeID: w.costCode, Location: "Km 12"})
		must(t, "log", err)
		must(t, "log submit", f.svc.SubmitEquipmentLog(f.ctx, w.eng, id))
		must(t, "log approve", f.svc.DecideEquipmentLog(f.ctx, w.pm, id, true, ""))
	}
	a, b := working("CH-A"), working("CH-B")
	logOf(a, "6.5", "1.5", "1000", "1008")
	logOf(b, "8", "0", "1000", "1008")
	run := ChargeRunInput{ProjectID: f.project, Month: month, CostAccountID: acct["6400"], RecoveryAccountID: acct["6490"]}

	// Nothing is charged at a guessed rate: both logs are unpriced.
	if _, err := f.svc.PrepareChargeRun(f.ctx, c.accountant, run); err == nil || !strings.Contains(err.Error(), "no approved charge rate") {
		t.Fatalf("a month was charged without approved rates: %v", err)
	}
	rate := func(eq, from, prod, idle string) string {
		id, err := f.svc.CreateChargeRate(f.ctx, w.officer, ChargeRateInput{EquipmentID: eq, EffectiveFrom: from, ProductiveRate: dec(prod), IdleRate: dec(idle), Basis: "Plant hire policy 2026 (test data)"})
		must(t, "rate", err)
		must(t, "rate submit", f.svc.SubmitChargeRate(f.ctx, w.officer, id))
		return id
	}
	ra := rate(a, "2026-01-01", "800", "200")
	selfApprover := w.manager
	selfApprover.UserID = w.officer.UserID
	if err := f.svc.DecideChargeRate(f.ctx, selfApprover, ra, true, ""); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("the preparer approved their own rate: %v", err)
	}
	rawRefusedWith(t, f, "approve a charge rate under a forged identity", "approval attestation",
		`UPDATE equipment_charge_rates SET status = 'APPROVED', approved_by = $2 WHERE id = $1::uuid`, ra, w.manager.UserID)
	stale := w.manager
	stale.AuthTime = time.Now().Add(-2 * time.Hour)
	if err := f.svc.DecideChargeRate(f.ctx, stale, ra, true, ""); err == nil {
		t.Fatal("a charge rate was approved without a fresh second factor")
	}
	must(t, "approve rate A", f.svc.DecideChargeRate(f.ctx, w.manager, ra, true, ""))
	// B's first rate starts tomorrow: today's log is still unpriced.
	must(t, "approve rate B later", f.svc.DecideChargeRate(f.ctx, w.manager, rate(b, today().AddDate(0, 0, 1).Format("2006-01-02"), "1200", "0"), true, ""))
	if _, err := f.svc.PrepareChargeRun(f.ctx, c.accountant, run); err == nil || !strings.Contains(err.Error(), "CH-B") {
		t.Fatalf("a log was charged at a rate not yet in force: %v", err)
	}
	must(t, "approve rate B", f.svc.DecideChargeRate(f.ctx, w.manager, rate(b, "2026-01-01", "1000", "250"), true, ""))

	pv, err := f.svc.PreviewChargeRun(f.ctx, c.accountant, f.project, month)
	must(t, "preview", err)
	if len(pv.Lines) != 2 || pv.Total.StringFixed(2) != "13500.00" {
		t.Fatalf("preview %+v, want two lines totalling 13,500.00", pv)
	}
	if _, err := f.svc.PrepareChargeRun(f.ctx, w.eng, run); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a site engineer prepared a charge run: %v", err)
	}
	if _, err := f.svc.PrepareChargeRun(f.ctx, c.accountant, ChargeRunInput{ProjectID: f.project, Month: month, CostAccountID: acct["1500"], RecoveryAccountID: acct["6490"]}); err == nil {
		t.Fatal("a charge run debited an asset account")
	}
	first, err := f.svc.PrepareChargeRun(f.ctx, c.accountant, run)
	must(t, "charge run", err)
	journalOf := func(run string) (jid, lines string) {
		must(t, "journal", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT r.journal_id::text, string_agg(a.code || ' ' || l.side || ' ' || l.amount::numeric(24,2) || ' ' || CASE WHEN l.project_id IS NULL THEN '-' ELSE 'project' END, '; ' ORDER BY a.code)
				FROM equipment_charge_runs r JOIN journal_lines l ON l.journal_id = r.journal_id JOIN accounts a ON a.id = l.account_id WHERE r.id = $1::uuid GROUP BY r.journal_id`, run).Scan(&jid, &lines)
		}))
		return
	}
	if _, lines := journalOf(first); lines != "6400 DEBIT 13500.00 project; 6490 CREDIT 13500.00 -" {
		t.Fatalf("charge journal lines: %s", lines)
	}
	// Each log is charged once: no second run; a line cannot be smuggled into another run later.
	if _, err := f.svc.PrepareChargeRun(f.ctx, c.accountant, run); err == nil {
		t.Fatal("the month was charged twice")
	}
	rawRefusedWith(t, f, "charge a log again in an existing run", "only by the transaction that prepares the run",
		`INSERT INTO equipment_charge_lines (run_id, log_id, equipment_id, rate_id, productive_hours, idle_hours, amount)
		 SELECT run_id, log_id, equipment_id, rate_id, productive_hours, idle_hours, amount FROM equipment_charge_lines WHERE run_id = $1::uuid LIMIT 1`, first)
	// Cancelled while its journal is a draft (finance), the logs are charged again; the posted journal stays bound.
	if err := f.svc.CancelChargeRun(f.ctx, c.accountant, first, "Wrong recovery account chosen"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("the preparer cancelled a run: %v", err)
	}
	must(t, "cancel", f.svc.CancelChargeRun(f.ctx, c.finMgr, first, "Wrong recovery account chosen"))
	second, err := f.svc.PrepareChargeRun(f.ctx, c.accountant, run)
	must(t, "charge again", err)
	jid, _ := journalOf(second)
	w.financePost(t, jid)
	if err := f.svc.CancelChargeRun(f.ctx, c.finMgr, second, "Too late to cancel this run"); err == nil {
		t.Fatal("a run whose journal is posted was cancelled")
	}
	rawRefusedWith(t, f, "alter a charge line", "permission denied",
		`UPDATE equipment_charge_lines SET amount = 1 WHERE run_id = '`+second+`'`)
}
