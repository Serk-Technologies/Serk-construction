package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// TestStock_SiteReturns (SUP-05.4, 0089): unused steel goes back from the
// project at the issue's cost; the preparer cannot approve and the tenant login
// cannot forge the approval; more than is still out is never returned; the
// STK_RETURN journal (Dr inventory, Cr the project's cost) equals the computed
// amount; returning all that is out gives back exactly the issue's amount;
// the reconciliation stays clean and the cost code's budget availability is
// back where it was before the issue.
func TestStock_SiteReturns(t *testing.T) {
	w := newStockWorld(t)
	f, c := w.f, w.cast
	w.stockUp(t) // 12 t at 84,500
	available := func() decimal.Decimal {
		return w.stockValue(t, `SELECT available FROM p2p_cost_position($1::uuid, NULL, NULL)`, w.costCode)
	}
	before := available()
	issued := w.issueApprovePost(t, w.itemSteel, "5") // 422,500
	var iss string
	must(t, "issue id", w.f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM stock_issues WHERE status = 'APPROVED' ORDER BY created_at DESC LIMIT 1`).Scan(&iss)
	}))
	// The steel was ordered on this cost code, so issuing it uses the code's own
	// inventory commitment: availability does not move. A return must not move it
	// either (before 0089 the return's credit raised it).
	if !available().Equal(before) {
		t.Fatalf("issuing the code's own stock changed its availability: %s, was %s", available(), before)
	}

	in := StockReturnInput{IssueID: iss, WarehouseID: w.store, Quantity: dec("2"), ReturnDate: today(), Reason: "Slab redesigned; bars not needed"}
	if _, err := f.svc.CreateStockReturn(f.ctx, c.estimator, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an estimator prepared a return: %v", err)
	}
	too := in
	too.Quantity = dec("6")
	big, err := f.svc.CreateStockReturn(f.ctx, c.store, too)
	must(t, "oversized draft", err)
	if _, err := f.svc.ApproveStockReturn(f.ctx, c.siteEng, big); err == nil || !strings.Contains(err.Error(), "still out") {
		t.Fatalf("6 t of a 5 t issue were returned: %v", err)
	}
	must(t, "cancel oversized", f.svc.CancelStockReturn(f.ctx, c.store, big, "Entered the wrong quantity"))

	r1, err := f.svc.CreateStockReturn(f.ctx, c.store, in)
	must(t, "return 2 t", err)
	if _, err := f.svc.ApproveStockReturn(f.ctx, c.store, r1); err == nil {
		t.Fatal("the preparer approved their own return")
	}
	rawRefusedWith(t, f, "approve a return by a forged update", "approval attestation",
		`UPDATE stock_returns SET status = 'APPROVED', approved_by = 'u-site-engineer' WHERE id = $1::uuid`, r1)
	j1, err := f.svc.ApproveStockReturn(f.ctx, c.siteEng, r1)
	must(t, "approve return", err)
	a1 := w.stockValue(t, `SELECT amount FROM stock_returns WHERE id = $1::uuid`, r1)
	if want := issued.Mul(dec("2")).Div(dec("5")).Round(2); !a1.Equal(want) {
		t.Fatalf("return valued %s, want %s (2/5 of the issue's %s)", a1, want, issued)
	}
	dr := w.stockValue(t, `SELECT sum(amount) FROM journal_lines WHERE journal_id = $1::uuid AND side = 'DEBIT' AND account_id = $2::uuid`, j1, w.acct["1200"])
	cr := w.stockValue(t, `SELECT sum(amount) FROM journal_lines WHERE journal_id = $1::uuid AND side = 'CREDIT' AND account_id = $2::uuid AND cost_code_id = $3::uuid`, j1, w.acct["5000"], w.costCode)
	if !dr.Equal(a1) || !cr.Equal(a1) {
		t.Fatalf("STK_RETURN journal Dr %s Cr %s, want %s", dr, cr, a1)
	}
	w.financePost(t, j1)

	// The rest comes back: exactly what remains of the issue's amount; then nothing more.
	in.Quantity = dec("3")
	r2, err := f.svc.CreateStockReturn(f.ctx, c.store, in)
	must(t, "return 3 t", err)
	j2, err := f.svc.ApproveStockReturn(f.ctx, c.siteEng, r2)
	must(t, "approve rest", err)
	a2 := w.stockValue(t, `SELECT amount FROM stock_returns WHERE id = $1::uuid`, r2)
	if !a1.Add(a2).Equal(issued) {
		t.Fatalf("returns %s + %s do not add up to the issue's %s", a1, a2, issued)
	}
	w.financePost(t, j2)
	in.Quantity = dec("0.1")
	r3, err := f.svc.CreateStockReturn(f.ctx, c.store, in)
	must(t, "one more draft", err)
	if _, err := f.svc.ApproveStockReturn(f.ctx, c.siteEng, r3); err == nil {
		t.Fatal("more was returned than was issued")
	}
	if q, _ := w.balanceOf(t, w.itemSteel); !q.Equal(dec("12")) {
		t.Fatalf("steel on hand %s after everything came back, want 12", q)
	}
	checks, err := f.svc.StockReconciliation(f.ctx, c.controller, f.entity)
	must(t, "reconciliation", err)
	for _, x := range checks {
		if !strings.HasPrefix(x.Check, "Received") && !x.Difference.IsZero() {
			t.Fatalf("reconciliation: %s differs by %s", x.Check, x.Difference)
		}
	}
	if after := available(); !after.Equal(before) {
		t.Fatalf("the code's availability is %s after everything came back, %s before the issue", after, before)
	}
}
