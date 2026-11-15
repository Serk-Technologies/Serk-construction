package kernel

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

// TestCashFlow_DirectMethodReference (CAP-01.5.b, 0091). Hand-computed
// reference, independent of the code under test. Fresh accounts:
//
//	C1 cash, C2 equipment (investing), C3 loan (financing), C4 owner's equity,
//	C5 sales, C6 site costs, C7 supplier payable (operating by default)
//
// Posted journals:
//
//	01 Oct  Dr C1 2,000   Cr C4 2,000    (before the period: opening cash 2,000)
//	03 Oct  Dr C1 10,000  Cr C3 10,000   financing  +10,000
//	05 Oct  Dr C2 3,000   Cr C1 3,000    investing   -3,000
//	10 Oct  Dr C1 4,000   Cr C5 4,000    operating   +4,000
//	15 Oct  Dr C6 1,500   Cr C1 1,500    operating   -1,500
//	20 Oct  Dr C6 700     Cr C7 700      no cash: not in the statement
//
// 2-31 October: operating 2,500, investing -3,000, financing 10,000; net 9,500;
// opening 2,000; closing 11,500.
func TestCashFlow_DirectMethodReference(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	acc := map[string]string{}
	must(t, "accounts", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		for _, a := range []struct{ code, typ string }{{"C1", "ASSET"}, {"C2", "ASSET"}, {"C3", "LIABILITY"}, {"C4", "EQUITY"}, {"C5", "REVENUE"}, {"C6", "EXPENSE"}, {"C7", "LIABILITY"}} {
			id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: f.tenant, LegalEntityID: f.entity, Code: "9" + a.code, Name: "Cash-flow test " + a.code, Type: ledger.AccountType(a.typ), Active: true})
			if err != nil {
				return err
			}
			acc[a.code] = id
		}
		return nil
	}))
	n := 0
	post := func(day, dr, cr, amount string) {
		t.Helper()
		n++
		at, _ := time.Parse("2006-01-02", day)
		j, err := f.svc.journalOf(f.ctx, c.accountant, f.entity, "", "cash-flow reference", "MANUAL", "", "cf-ref-"+day+"-"+dr+cr, at,
			[]JournalLineInput{{AccountID: acc[dr], Side: "DEBIT", Amount: dec(amount)}, {AccountID: acc[cr], Side: "CREDIT", Amount: dec(amount)}})
		must(t, "journal", err)
		res, err := f.svc.CreateJournal(f.ctx, c.accountant, j)
		must(t, "create journal", err)
		w.financePost(t, res.JournalID)
	}
	post("2026-10-01", "C1", "C4", "2000")
	post("2026-10-03", "C1", "C3", "10000")
	post("2026-10-05", "C2", "C1", "3000")
	post("2026-10-10", "C1", "C5", "4000")
	post("2026-10-15", "C6", "C1", "1500")
	post("2026-10-20", "C6", "C7", "700")
	from, to := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)

	// Nothing is cash until a class is approved.
	cf, err := f.svc.CashFlow(f.ctx, c.controller, f.entity, from, to)
	must(t, "statement before classes", err)
	if cf.CashAccounts != 0 || !cf.NetChange.IsZero() {
		t.Fatalf("cash before any approved class: %+v", cf)
	}

	// Classes: proposed by the controller (account.manage), approved by the finance manager.
	if _, err := f.svc.ProposeCashFlowClass(f.ctx, c.accountant, acc["C1"], "CASH"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an accountant proposed a class: %v", err)
	}
	ids := map[string]string{}
	for code, class := range map[string]string{"C1": "CASH", "C2": "INVESTING", "C3": "FINANCING"} {
		id, err := f.svc.ProposeCashFlowClass(f.ctx, c.controller, acc[code], class)
		must(t, "propose "+code, err)
		ids[code] = id
	}
	rawRefusedWith(t, f, "approve a class by a forged update", "approval attestation",
		`UPDATE cash_flow_classes SET status = 'APPROVED', approved_by = 'u-finance-manager' WHERE id = $1::uuid`, ids["C1"])
	rawRefusedWith(t, f, "the proposer approves by a direct update", "segregation of duties",
		`UPDATE cash_flow_classes SET status = 'APPROVED', approved_by = 'u-controller' WHERE id = $1::uuid`, ids["C1"])
	controllerAndApprover := c.finMgr
	controllerAndApprover.UserID = c.controller.UserID
	if err := f.svc.ApproveCashFlowClass(f.ctx, controllerAndApprover, ids["C1"]); err == nil {
		t.Fatal("the proposer approved their own class")
	}
	for _, id := range ids {
		must(t, "approve", f.svc.ApproveCashFlowClass(f.ctx, c.finMgr, id))
	}
	rawFails(t, f, "delete an approved class", `DELETE FROM cash_flow_classes WHERE id = '`+ids["C1"]+`'`)

	cf, err = f.svc.CashFlow(f.ctx, c.controller, f.entity, from, to)
	must(t, "statement", err)
	want := map[string]string{"operating": "2500", "investing": "-3000", "financing": "10000", "net": "9500", "opening": "2000", "closing": "11500"}
	got := map[string]string{"operating": cf.TotalOperating.String(), "investing": cf.TotalInvesting.String(), "financing": cf.TotalFinancing.String(),
		"net": cf.NetChange.String(), "opening": cf.OpeningCash.String(), "closing": cf.ClosingCash.String()}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %s, want %s", k, got[k], v)
		}
	}
	if !cf.Reconciles || cf.CashAccounts != 1 {
		t.Fatalf("statement: %+v", cf)
	}
	if len(cf.Operating) != 2 || len(cf.Investing) != 1 || len(cf.Financing) != 1 || cf.Financing[0].AccountID != acc["C3"] {
		t.Fatalf("lines: %+v", cf)
	}
	if _, err := f.svc.CashFlow(f.ctx, c.pm, f.entity, from, to); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project manager read the cash-flow statement: %v", err)
	}
}
