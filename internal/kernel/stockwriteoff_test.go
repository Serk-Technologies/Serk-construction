package kernel

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/serk-group/serk-construction/pkg/auth"
)

// TestStock_QuarantineAndWriteoff (SUP-05.4, 0088): stock moves into a
// quarantine store by an approved transfer; nothing is issued or reserved from
// it; a write-off with a reason, approved by someone other than its preparer
// (attested), removes it at FIFO cost and generates the STK_SCRAP journal
// (Dr write-off expense, Cr inventory) bound to that amount; stock value and
// layers still agree afterwards.
func TestStock_QuarantineAndWriteoff(t *testing.T) {
	w := newStockWorld(t)
	f, c := w.f, w.cast
	w.stockUp(t) // 12 t of steel in the site store
	pmgr := auth.Principal{Kind: auth.KindHuman, UserID: "u-proc-manager", TenantID: f.tenant, Roles: []string{"procurement_manager"}, ProjectIDs: []string{f.project}, IssuedAt: time.Now().Add(-time.Minute)}
	quar, err := f.svc.CreateQuarantineWarehouse(f.ctx, pmgr, f.entity, f.project, "P001-QUAR", "Project 001 quarantine bay")
	must(t, "quarantine store", err)
	rawFails(t, f, "turn a quarantine store into an ordinary one", `UPDATE warehouses SET quarantine = false WHERE id = '`+quar+`'`)

	// Into quarantine: an ordinary transfer, approved by the inventory controller.
	tr, err := f.svc.CreateStockTransfer(f.ctx, c.store, StockTransferInput{StockItemID: w.itemSteel, FromWarehouseID: w.store, ToWarehouseID: quar,
		Quantity: dec("3"), TransferDate: today(), Note: "Corroded bars found at delivery inspection"})
	must(t, "transfer to quarantine", err)
	must(t, "approve transfer", f.svc.ApproveStockTransfer(f.ctx, w.invCtl, tr))

	// Nothing leaves quarantine to a project.
	if _, err := f.svc.CreateStockIssue(f.ctx, c.store, StockIssueInput{WarehouseID: quar, StockItemID: w.itemSteel, ProjectID: f.project,
		CostCodeID: w.costCode, ExpenseAccountID: w.acct["5000"], Quantity: dec("1"), IssueDate: today()}); err == nil || !strings.Contains(err.Error(), "quarantine") {
		t.Fatalf("stock was issued from quarantine: %v", err)
	}
	if _, err := f.svc.ReserveStock(f.ctx, c.siteEng, w.itemSteel, quar, f.project, dec("1"), "", "Level 4", time.Now().Add(72*time.Hour)); err == nil {
		t.Fatal("stock in quarantine was reserved")
	}

	// Write-off of 2 t from quarantine.
	in := StockWriteoffInput{WarehouseID: quar, StockItemID: w.itemSteel, ExpenseAccountID: w.acct["5200"], Quantity: dec("2"), WriteoffDate: today(), Reason: "Corroded beyond use; scrapped to the metal dealer"}
	if _, err := f.svc.CreateStockWriteoff(f.ctx, c.siteEng, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a site engineer prepared a write-off: %v", err)
	}
	bad := in
	bad.Reason = "bad"
	if _, err := f.svc.CreateStockWriteoff(f.ctx, c.store, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a write-off without a reason: %v", err)
	}
	wo, err := f.svc.CreateStockWriteoff(f.ctx, c.store, in)
	must(t, "write-off", err)
	if _, err := f.svc.ApproveStockWriteoff(f.ctx, c.store, wo); err == nil {
		t.Fatal("the preparer approved their own write-off")
	}
	rawRefusedWith(t, f, "approve a write-off by a forged update", "approval attestation",
		`UPDATE stock_writeoffs SET status = 'APPROVED', approved_by = 'u-inventory-controller' WHERE id = $1::uuid`, wo)
	beforeQty, beforeVal := w.balanceOf(t, w.itemSteel)
	jid, err := f.svc.ApproveStockWriteoff(f.ctx, w.invCtl, wo)
	must(t, "approve write-off", err)
	afterQty, afterVal := w.balanceOf(t, w.itemSteel)
	amount := w.stockValue(t, `SELECT amount FROM stock_writeoffs WHERE id = $1::uuid`, wo)
	consumed := w.stockValue(t, `SELECT sum(value) FROM stock_consumptions WHERE doc_type = 'WRITEOFF' AND doc_id = $1::uuid`, wo)
	if !beforeQty.Sub(afterQty).Equal(dec("2")) || !beforeVal.Sub(afterVal).Equal(amount) || !amount.Equal(consumed) || !amount.IsPositive() {
		t.Fatalf("write-off: qty %s -> %s, value %s -> %s, amount %s, consumed %s", beforeQty, afterQty, beforeVal, afterVal, amount, consumed)
	}
	dr := w.stockValue(t, `SELECT sum(amount) FROM journal_lines WHERE journal_id = $1::uuid AND side = 'DEBIT' AND account_id = $2::uuid`, jid, w.acct["5200"])
	cr := w.stockValue(t, `SELECT sum(amount) FROM journal_lines WHERE journal_id = $1::uuid AND side = 'CREDIT' AND account_id = $2::uuid`, jid, w.acct["1200"])
	if !dr.Equal(amount) || !cr.Equal(amount) {
		t.Fatalf("STK_SCRAP journal: Dr %s Cr %s, want %s", dr, cr, amount)
	}
	// The inventory reconciliation counts the write-off: no break before or after posting.
	recon := func(when string) {
		t.Helper()
		checks, err := f.svc.StockReconciliation(f.ctx, c.controller, f.entity)
		must(t, "reconciliation", err)
		for _, x := range checks {
			if !strings.HasPrefix(x.Check, "Received") && !x.Difference.IsZero() {
				t.Fatalf("reconciliation %s: %s differs by %s", when, x.Check, x.Difference)
			}
		}
	}
	recon("before posting")
	w.financePost(t, jid)
	recon("after posting")
	rawFails(t, f, "edit an approved write-off", `UPDATE stock_writeoffs SET quantity = 1 WHERE id = '`+wo+`'`)
	if bal, layers := w.stockValue(t, `SELECT COALESCE(sum(value),0) FROM stock_balances WHERE stock_item_id = $1::uuid`, w.itemSteel),
		w.stockValue(t, `SELECT COALESCE(sum(value_remaining),0) FROM stock_layers WHERE stock_item_id = $1::uuid`, w.itemSteel); !bal.Equal(layers) {
		t.Fatalf("balances %s and layers %s disagree after the write-off", bal, layers)
	}

	// The last tonne is released back by transfer; a second write-off is cancelled by its preparer.
	back, err := f.svc.CreateStockTransfer(f.ctx, c.store, StockTransferInput{StockItemID: w.itemSteel, FromWarehouseID: quar, ToWarehouseID: w.store, Quantity: dec("1"), TransferDate: today(), Note: "Cleaned and accepted"})
	must(t, "release", err)
	must(t, "approve release", f.svc.ApproveStockTransfer(f.ctx, w.invCtl, back))
	in.WarehouseID, in.Quantity = w.store, dec("1")
	wo2, err := f.svc.CreateStockWriteoff(f.ctx, c.store, in)
	must(t, "write-off 2", err)
	if err := f.svc.RejectStockWriteoff(f.ctx, w.invCtl, wo2, ""); err == nil {
		t.Fatal("a write-off was rejected without a reason")
	}
	must(t, "cancel write-off", f.svc.CancelStockWriteoff(f.ctx, c.store, wo2, "Counted again: the bar is fine"))
	if q, _ := w.balanceOf(t, w.itemSteel); !q.Equal(dec("10")) {
		t.Fatalf("steel on hand %s, want 10", q)
	}
}
