package kernel

// Tests for units of measure, stock reservations and reorder rules (WP-1.6,
// migration 0041, kernel/stockplus.go). What is proven:
//  1. conversions are controlled (maker/checker, attested) and exact;
//  2. a reservation never exceeds unreserved stock, even when two people race
//     for the last units, and the database refuses forged changes;
//  3. an issue cannot eat into another reservation, one made against a
//     reservation consumes it and cannot exceed it;
//  4. release rules, and the low-stock report with its suggestion.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func TestStockPlus_ConversionsAreControlled(t *testing.T) {
	w := newStockWorld(t)
	f, c := w.f, w.cast

	// Nothing is converted until an approved conversion exists.
	if _, err := f.svc.ToBaseUnit(f.ctx, c.store, w.itemSteel, dec("4"), "bundle"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("conversion without an approved factor: %v", err)
	}
	if _, err := f.svc.CreateUomConversion(f.ctx, c.buyer, "", "bundle", "t", dec("0"), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a zero factor was accepted: %v", err)
	}
	// An item-specific conversion must convert into that item's own base unit (DB rule).
	if _, err := f.svc.CreateUomConversion(f.ctx, c.buyer, w.itemSteel, "bundle", "bag", dec("2.5"), ""); err == nil {
		t.Fatal("a steel conversion into bags (not its base unit) was accepted")
	}
	id, err := f.svc.CreateUomConversion(f.ctx, c.buyer, "", "bundle", "t", dec("2.5"), "A bundle of rebar is 2.5 t")
	must(t, "create conversion", err)
	must(t, "submit conversion", f.svc.SubmitUomConversion(f.ctx, c.buyer, id))
	if err := f.svc.DecideUomConversion(f.ctx, c.buyer, id, true, ""); err == nil {
		t.Fatal("the author approved their own conversion")
	}
	if _, err := f.svc.ToBaseUnit(f.ctx, c.store, w.itemSteel, dec("4"), "bundle"); err == nil {
		t.Fatal("an unapproved conversion was applied")
	}
	must(t, "approve conversion", f.svc.DecideUomConversion(f.ctx, c.finMgr, id, true, ""))
	got, err := f.svc.ToBaseUnit(f.ctx, c.store, w.itemSteel, dec("4"), "bundle")
	must(t, "convert", err)
	if !got.Equal(dec("10")) {
		t.Fatalf("4 bundles = %s t, want 10", got)
	}
	if got, _ := f.svc.ToBaseUnit(f.ctx, c.store, w.itemSteel, dec("4"), "t"); !got.Equal(dec("4")) {
		t.Fatalf("base unit changed the quantity: %s", got)
	}
	// One approved factor per pair: a second approved bundle->t is refused by the database.
	id2, err := f.svc.CreateUomConversion(f.ctx, c.buyer, "", "Bundle", "t", dec("3"), "")
	must(t, "second conversion", err)
	must(t, "submit second", f.svc.SubmitUomConversion(f.ctx, c.buyer, id2))
	if err := f.svc.DecideUomConversion(f.ctx, c.finMgr, id2, true, ""); err == nil {
		t.Fatal("two approved factors for the same pair were accepted")
	}
	// Forgery with raw SQL as the tenant login.
	for name, q := range map[string]string{
		"approve a conversion without an attestation": `UPDATE uom_conversions SET status = 'APPROVED', approved_by = 'u-forger' WHERE id = '` + id2 + `'`,
		"change an approved factor":                   `UPDATE uom_conversions SET factor = 100 WHERE id = '` + id + `'`,
		"insert an approved conversion":               `INSERT INTO uom_conversions (from_uom, to_uom, factor, status, created_by, approved_by) VALUES ('a', 'b', 1, 'APPROVED', 'x', 'y')`,
	} {
		err := f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, q); return err })
		if err == nil {
			t.Fatalf("database accepted: %s", name)
		}
	}
	// An issue counted in bundles is converted: 1 bundle = 2.5 t.
	w.stockUp(t)
	is, err := f.svc.CreateStockIssue(f.ctx, c.store, StockIssueInput{WarehouseID: w.store, StockItemID: w.itemSteel, ProjectID: f.project,
		CostCodeID: w.costCode, ExpenseAccountID: w.acct["5000"], Quantity: dec("1"), Unit: "bundle", IssueDate: today()})
	must(t, "issue in bundles", err)
	if q := w.stockValue(t, `SELECT quantity FROM stock_issues WHERE id = $1::uuid`, is); !q.Equal(dec("2.5")) {
		t.Fatalf("1 bundle issued as %s t, want 2.5", q)
	}
}

// stockUp puts 12 t of steel on the shelf of the project store.
func (w *stockWorld) stockUp(t *testing.T) {
	t.Helper()
	po := w.inventoryPO(t, "20", "500", "84500", "940")
	grn, _ := w.receipt(t, po, GRNLine{POLineNo: 1, QtyReceived: dec("12")})
	w.putAway(t, grn, 1, w.itemSteel, "12")
}

func TestStockPlus_ReservationsIssuesAndReorder(t *testing.T) {
	w := newStockWorld(t)
	f, c := w.f, w.cast
	w.stockUp(t) // 12 t on hand
	reserve := func(who auth.Principal, qty string) (string, error) {
		return f.svc.ReserveStock(f.ctx, who, w.itemSteel, w.store, f.project, dec(qty), "", "Level 3 slab", time.Now().Add(14*24*time.Hour))
	}

	// Only people who may reserve can; the quantity must fit the unreserved stock.
	if _, err := reserve(c.estimator, "1"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an estimator reserved stock: %v", err)
	}
	r1, err := reserve(c.siteEng, "8")
	must(t, "reserve 8", err)
	if _, err := reserve(c.siteEng, "5"); err == nil {
		t.Fatal("reserved 5 t with only 4 t unreserved")
	}
	// Two people race for the last 4 t: exactly one wins.
	var wg sync.WaitGroup
	results := make([]error, 2)
	ids := make([]string, 2)
	start := make(chan struct{})
	for i, who := range []auth.Principal{c.siteEng, c.pm} {
		wg.Add(1)
		go func(i int, who auth.Principal) {
			defer wg.Done()
			<-start
			ids[i], results[i] = reserve(who, "3")
		}(i, who)
	}
	close(start)
	wg.Wait()
	won := -1
	for i, e := range results {
		if e == nil {
			if won >= 0 {
				// both fit (4 >= 3+3 is false, so two wins must be impossible)
				t.Fatalf("two reservations of 3 t both succeeded with 4 t unreserved: %v", results)
			}
			won = i
		}
	}
	if won < 0 {
		t.Fatalf("neither racing reservation succeeded: %v", results)
	}
	r2 := ids[won]

	// An unreserved issue may not eat into the reservations (12 on hand, 11 reserved).
	is, err := w.issue(t, w.itemSteel, "2")
	must(t, "issue 2", err)
	if _, err := f.svc.ApproveStockIssue(f.ctx, c.siteEng, is); err == nil {
		t.Fatal("an issue used stock reserved for others")
	} else {
		t.Logf("held: %v", err)
	}
	// An issue against the reservation consumes it.
	mine, err := f.svc.CreateStockIssue(f.ctx, c.store, StockIssueInput{WarehouseID: w.store, StockItemID: w.itemSteel, ProjectID: f.project,
		CostCodeID: w.costCode, ExpenseAccountID: w.acct["5000"], Quantity: dec("8"), ReservationID: r1, IssueDate: today()})
	must(t, "issue against reservation", err)
	_, err = f.svc.ApproveStockIssue(f.ctx, c.siteEng, mine)
	must(t, "approve issue against reservation", err)
	rs, err := f.svc.ListReservations(f.ctx, c.siteEng, "CONSUMED")
	must(t, "list consumed", err)
	if len(rs) != 1 || rs[0].ID != r1 {
		t.Fatalf("consumed reservations = %+v, want %s", rs, r1)
	}
	// The consumed reservation is final; a second issue against it is refused.
	again, err := f.svc.CreateStockIssue(f.ctx, c.store, StockIssueInput{WarehouseID: w.store, StockItemID: w.itemSteel, ProjectID: f.project,
		CostCodeID: w.costCode, ExpenseAccountID: w.acct["5000"], Quantity: dec("1"), ReservationID: r1, IssueDate: today()})
	must(t, "second issue draft", err)
	if _, err := f.svc.ApproveStockIssue(f.ctx, c.siteEng, again); err == nil {
		t.Fatal("a consumed reservation was used twice")
	}
	// An issue cannot exceed its reservation: r2 holds 3, issuing 4 against it is refused.
	over, err := f.svc.CreateStockIssue(f.ctx, c.store, StockIssueInput{WarehouseID: w.store, StockItemID: w.itemSteel, ProjectID: f.project,
		CostCodeID: w.costCode, ExpenseAccountID: w.acct["5000"], Quantity: dec("4"), ReservationID: r2, IssueDate: today()})
	must(t, "over draft", err)
	if _, err := f.svc.ApproveStockIssue(f.ctx, c.siteEng, over); err == nil {
		t.Fatal("an issue exceeded its reservation")
	}

	// Release: a stranger cannot; the person who reserved it can, with a reason, once.
	stranger := auth.Principal{Kind: auth.KindHuman, UserID: "u-other", TenantID: f.tenant, Roles: []string{"estimator"}, ProjectIDs: []string{f.project}}
	if err := f.svc.ReleaseReservation(f.ctx, stranger, r2, "not mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a stranger released a reservation: %v", err)
	}
	who := c.siteEng
	if won == 1 {
		who = c.pm
	}
	if err := f.svc.ReleaseReservation(f.ctx, who, r2, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("release without a reason: %v", err)
	}
	must(t, "release", f.svc.ReleaseReservation(f.ctx, who, r2, "Pour postponed"))
	if err := f.svc.ReleaseReservation(f.ctx, who, r2, "again"); err == nil {
		t.Fatal("a released reservation was released twice")
	}
	// With nothing reserved, the unreserved issue (2 t) now goes through.
	if _, err := f.svc.ApproveStockIssue(f.ctx, c.siteEng, is); err != nil {
		t.Fatalf("unreserved issue after release: %v", err)
	}

	// Forgery: the tenant login cannot consume or invent a reservation.
	live, err := reserve(c.siteEng, "1")
	must(t, "live reservation", err)
	for name, q := range map[string]string{
		"consume an active reservation by hand":          `UPDATE stock_reservations SET status = 'CONSUMED' WHERE id = '` + live + `'`,
		"release an active reservation without a reason": `UPDATE stock_reservations SET status = 'RELEASED', released_by = 'u-x' WHERE id = '` + live + `'`,
		"consume a reservation by hand":                  `UPDATE stock_reservations SET status = 'CONSUMED' WHERE id = '` + r2 + `'`,
		"reserve more than is on hand":                   `INSERT INTO stock_reservations (stock_item_id, warehouse_id, project_id, quantity, purpose, expires_at, created_by) VALUES ('` + w.itemSteel + `', '` + w.store + `', '` + f.project + `', 1000, 'forged', now() + interval '1 day', 'x')`,
		"reserve with an expiry in the past":             `INSERT INTO stock_reservations (stock_item_id, warehouse_id, project_id, quantity, purpose, expires_at, created_by) VALUES ('` + w.itemSteel + `', '` + w.store + `', '` + f.project + `', 1, 'forged', now() - interval '1 day', 'x')`,
		"reactivate a released reservation":              `UPDATE stock_reservations SET status = 'ACTIVE' WHERE id = '` + r2 + `'`,
	} {
		err := f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, q); return err })
		if err == nil {
			t.Fatalf("database accepted: %s", name)
		}
		t.Logf("held: %s: %v", name, err)
	}

	// Reorder rule and the low-stock report: on hand 2 t (12 - 8 - 2) of which 1 t is reserved
	// (the live reservation above), minimum 10, maximum 20, reorder 5.
	if err := f.svc.SetReorderRule(f.ctx, c.estimator, ReorderRule{StockItemID: w.itemSteel, WarehouseID: w.store, Min: dec("10"), Max: dec("20"), ReorderQty: dec("5")}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("an estimator set a reorder rule: %v", err)
	}
	if err := f.svc.SetReorderRule(f.ctx, c.buyer, ReorderRule{StockItemID: w.itemSteel, WarehouseID: w.store, Min: dec("10"), Max: dec("5"), ReorderQty: dec("5")}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a maximum below the minimum was accepted: %v", err)
	}
	must(t, "reorder rule", f.svc.SetReorderRule(f.ctx, c.buyer, ReorderRule{StockItemID: w.itemSteel, WarehouseID: w.store, Min: dec("10"), Max: dec("20"), ReorderQty: dec("5")}))
	must(t, "reorder rule update", f.svc.SetReorderRule(f.ctx, c.buyer, ReorderRule{StockItemID: w.itemSteel, WarehouseID: w.store, Min: dec("10"), Max: dec("20"), ReorderQty: dec("5")}))
	low, err := f.svc.RunReport(f.ctx, c.cfo, "low-stock", ReportFilters{EntityID: f.entity})
	must(t, "low stock", err)
	if len(low.Rows) != 1 {
		t.Fatalf("low-stock rows = %v", low.Rows)
	}
	row := low.Rows[0]
	avail := dec(row["available"])
	want := decimal.Max(dec("5"), dec("20").Sub(avail))
	if !avail.Equal(dec("1")) || !dec(row["suggested"]).Equal(want) || !dec(row["on_hand"]).Equal(dec("2")) || !dec(row["reserved"]).Equal(dec("1")) {
		t.Fatalf("low-stock row = %v (available %s, suggested should be %s)", row, avail, want)
	}
}
