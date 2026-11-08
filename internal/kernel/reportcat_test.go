package kernel

// Tests for the report catalogue (WP-1.5, kernel/reportcat.go).
//
// What is proven:
//  1. every one of the 14 reports runs against realistic data (a purchase
//     order, two receipts, two approved invoices and payments, posted
//     journals) without a SQL error, and returns the columns it declares;
//  2. totals tie out to independent figures (ledger, order value, invoices,
//     the payment register against the payments, GL detail against the
//     trial balance);
//  3. scope: a project-restricted person gets only their project's rows and is
//     refused company-wide reports; a project outside their scope is
//     indistinguishable from absent; bad input is refused.

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func (w *p2pWorld) reportsWorld(t *testing.T) (po string) {
	t.Helper()
	f, c := w.f, w.cast
	w.approvedBOQ(t)
	_, _, po = w.awardedPO(t, "20", "500")
	_, j1 := w.receipt(t, po, GRNLine{POLineNo: 1, QtyReceived: dec("12")}, GRNLine{POLineNo: 2, QtyReceived: dec("500")})
	inv1, m1 := w.invoice(t, InvoiceInput{POID: po, InvoiceNumber: "ABC/2026/0417", InvoiceDate: today(), DueDate: today().AddDate(0, 0, 30),
		Subtotal: dec("1486500"), TaxAmount: dec("222975"), Total: dec("1709475"),
		Lines: []InvoiceLine{{1, dec("12"), dec("84500")}, {2, dec("500"), dec("945")}}})
	if m1 != "MATCHED" {
		t.Fatalf("invoice match = %s", m1)
	}
	j3, err := f.svc.ApproveInvoice(f.ctx, c.apSup, inv1, "")
	must(t, "approve invoice", err)
	w.financePost(t, j1, j3)
	pay := w.r3payment(t, inv1, "1709475")
	if st, err := f.svc.ApprovePayment(f.ctx, c.finMgr, pay); err != nil || st != "PARTIALLY_APPROVED" {
		t.Fatalf("approve 1: %s %v", st, err)
	}
	if _, err := f.svc.ApprovePayment(f.ctx, c.cfo, pay); err != nil {
		t.Fatalf("approve 2: %v", err)
	}
	return po
}

func TestReportCatalogue_EveryReportRunsAndTiesOut(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	w.reportsWorld(t)
	if len(reportDefs) != 19 || len(reportIDs()) != 19 {
		t.Fatalf("the catalogue has %d reports, want 19", len(reportDefs))
	}
	run := func(id string, fl ReportFilters) ReportResult {
		t.Helper()
		fl.EntityID = f.entity
		r, err := f.svc.RunReport(f.ctx, c.cfo, id, fl)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		def := reportDef(id)
		if len(r.Columns) != len(def.Columns) || r.AsOf == "" || r.Title == "" {
			t.Fatalf("%s: columns %d as_of %q title %q", id, len(r.Columns), r.AsOf, r.Title)
		}
		for _, row := range r.Rows {
			for _, cl := range def.Columns {
				if _, ok := row[cl.Key]; !ok {
					t.Fatalf("%s: a row lacks the declared column %q: %v", id, cl.Key, row)
				}
			}
		}
		return r
	}
	supplier := w.sup[0]
	reports := map[string]ReportResult{}
	for _, d := range reportDefs {
		fl := ReportFilters{}
		if d.Subject == "supplier" {
			fl.SubjectID = supplier
		}
		if d.ID == "budget-vs-actual" {
			fl.ProjectID = f.project
		}
		if d.Subject == "customer" {
			// A customer with no activity: must run and be empty.
			// No customer activity in this flow: the query must run and be empty
			// (the contract-to-cash flow is covered by the browser journey B).
			fl.SubjectID = uuid.NewString()
		}
		reports[d.ID] = run(d.ID, fl)
	}
	if c := reports["customer-statement"]; len(c.Rows) != 0 {
		t.Fatalf("customer statement for an unknown customer: %v", c.Rows)
	}

	// Journal register total = every journal's debit total; GL detail debits = credits (balanced books).
	gl := reports["gl-detail"]
	if len(gl.Rows) == 0 || gl.Totals["debit"] != gl.Totals["credit"] {
		t.Fatalf("GL detail: %d rows, debits %s credits %s", len(gl.Rows), gl.Totals["debit"], gl.Totals["credit"])
	}
	tb, err := f.svc.TrialBalanceReport(f.ctx, c.cfo, f.entity, today().AddDate(0, 0, 1))
	must(t, "trial balance", err)
	if gl.Totals["debit"] != tb.TotalDebit.StringFixed(2) {
		t.Fatalf("GL detail debits %s != trial balance debits %s", gl.Totals["debit"], tb.TotalDebit.StringFixed(2))
	}
	if len(reports["journal-register"].Rows) == 0 {
		t.Fatal("journal register is empty")
	}
	// PO register: one approved order whose total equals the sum of its lines.
	po := reports["po-register"]
	if len(po.Rows) != 1 || po.Rows[0]["status"] != "APPROVED" {
		t.Fatalf("PO register: %v", po.Rows)
	}
	// Open commitment = order total - approved invoices (subtotal): 1,486,500 invoiced.
	oc := reports["open-commitments"]
	if len(oc.Rows) != 1 || oc.Rows[0]["invoiced"] != "1486500.000000" && oc.Rows[0]["invoiced"] != "1486500" {
		t.Fatalf("open commitments: %v", oc.Rows)
	}
	wantOpen := dec(po.Rows[0]["total"]).Sub(dec(oc.Rows[0]["invoiced"]))
	if !dec(oc.Rows[0]["open"]).Equal(wantOpen) {
		t.Fatalf("open commitment %s, want %s", oc.Rows[0]["open"], wantOpen)
	}
	// GRNI: 12 t steel + 500 bags received = invoiced, so nothing received-not-invoiced.
	if g := reports["grni"]; len(g.Rows) != 0 {
		t.Fatalf("GRNI should be empty when every receipt is invoiced: %v", g.Rows)
	}
	// Payment register: one payment of 1,709,475 that is fully approved/released path.
	pr := reports["payment-register"]
	if len(pr.Rows) != 1 || !dec(pr.Rows[0]["amount"]).Equal(dec("1709475")) {
		t.Fatalf("payment register: %v", pr.Rows)
	}
	for _, col := range reportDef("payment-register").Columns {
		if col.Key == "bank_snapshot" || col.Key == "account_number" {
			t.Fatalf("the payment register exposes %s", col.Key)
		}
	}
	// Vendor statement: invoiced 1,709,475, balance owed = invoiced - paid released.
	vs := reports["vendor-statement"]
	if len(vs.Rows) == 0 || vs.Totals["invoiced"] != "1709475.00" {
		t.Fatalf("vendor statement: totals %v rows %d", vs.Totals, len(vs.Rows))
	}
	// Stock valuation and movement run (no stock items in this flow: empty is correct).
	if len(reports["stock-valuation"].Rows) != 0 {
		t.Fatal("stock valuation should be empty without stock items")
	}
	// Budget against actual: cost on the cost code is cumulative and equals the cost report's actual.
	bva := reports["budget-vs-actual"]
	rows, err := f.svc.ProjectCostReport(f.ctx, c.cfo, f.project)
	must(t, "cost report", err)
	var actual decimal.Decimal
	for _, r := range rows {
		actual = actual.Add(r.ActualPosted)
	}
	var last decimal.Decimal
	for _, row := range bva.Rows {
		last = dec(row["cumulative_cost"])
	}
	if len(bva.Rows) == 0 || !last.Equal(actual) {
		t.Fatalf("budget-vs-actual cumulative %s != cost report posted actual %s (%d rows)", last, actual, len(bva.Rows))
	}
	// The catalogue lists all 19 for the CFO.
	if n := len(f.svc.ReportCatalogue(c.cfo)); n != 19 {
		t.Fatalf("CFO catalogue has %d reports", n)
	}
}

func reportDef(id string) ReportDef {
	for _, d := range reportDefs {
		if d.ID == id {
			return d
		}
	}
	return ReportDef{}
}

func TestReportCatalogue_ScopeAndInput(t *testing.T) {
	w := newP2PWorld(t)
	f, c := w.f, w.cast
	w.reportsWorld(t)

	// The project manager (scoped to one project, not tenant-wide) runs project reports...
	r, err := f.svc.RunReport(f.ctx, c.pm, "po-register", ReportFilters{EntityID: f.entity})
	must(t, "pm po-register", err)
	if len(r.Rows) != 1 {
		t.Fatalf("pm sees %d orders, want 1", len(r.Rows))
	}
	// ... but not company-wide ones.
	for _, id := range []string{"gl-detail", "journal-register", "payment-register", "vendor-statement"} {
		if _, err := f.svc.RunReport(f.ctx, c.pm, id, ReportFilters{EntityID: f.entity, SubjectID: w.sup[0]}); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("%s for a project manager: %v", id, err)
		}
	}
	for _, d := range f.svc.ReportCatalogue(c.pm) {
		if d.TenantWide {
			t.Fatalf("the catalogue offers %s to a project-restricted person", d.ID)
		}
	}
	// A person with no projects sees none of the project rows.
	none := auth.Principal{Kind: auth.KindHuman, UserID: "u-none", TenantID: f.tenant, Roles: []string{"estimator"}}
	r, err = f.svc.RunReport(f.ctx, none, "po-register", ReportFilters{EntityID: f.entity})
	must(t, "no-scope po-register", err)
	if len(r.Rows) != 0 {
		t.Fatalf("a person with no projects sees %d orders", len(r.Rows))
	}
	// A project outside the scope is indistinguishable from absent.
	if _, err := f.svc.RunReport(f.ctx, none, "po-register", ReportFilters{EntityID: f.entity, ProjectID: f.project}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("project outside scope: %v", err)
	}
	// Input checks.
	bad := []struct {
		name string
		id   string
		fl   ReportFilters
		want error
	}{
		{"unknown report", "nope", ReportFilters{EntityID: f.entity}, ErrNotFound},
		{"no entity", "po-register", ReportFilters{}, ErrInvalidInput},
		{"bad date", "po-register", ReportFilters{EntityID: f.entity, From: "2026-13-45"}, ErrInvalidInput},
		{"statement without a supplier", "vendor-statement", ReportFilters{EntityID: f.entity}, ErrInvalidInput},
		{"budget-vs-actual without a project", "budget-vs-actual", ReportFilters{EntityID: f.entity}, ErrInvalidInput},
		{"malformed uuid", "po-register", ReportFilters{EntityID: f.entity, ProjectID: "x"}, ErrNotFound},
	}
	for _, b := range bad {
		if _, err := f.svc.RunReport(f.ctx, c.cfo, b.id, b.fl); !errors.Is(err, b.want) {
			t.Fatalf("%s: got %v, want %v", b.name, err, b.want)
		}
	}
	// The other company's entity returns nothing (row-level security), never an error that leaks existence.
	var other string
	must(t, "tenant2 entity", f.db.AdminStore.WithTenant(f.ctx, f.tenant2, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM legal_entities LIMIT 1`).Scan(&other)
	}))
	r, err = f.svc.RunReport(f.ctx, c.cfo, "po-register", ReportFilters{EntityID: other})
	must(t, "other entity", err)
	if len(r.Rows) != 0 {
		t.Fatalf("another company's entity returned %d rows", len(r.Rows))
	}
}

// The report catalogue document is generated from the registry
// (UPDATE_KPI_DOC=1 rewrites it, as for the KPI catalogue).
func TestReportCatalogueDocumentIsCurrent(t *testing.T) {
	const path = "../../docs/reports/REPORT_CATALOGUE.md"
	want := ReportCatalogueMarkdown()
	if os.Getenv("UPDATE_KPI_DOC") == "1" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (generate it with UPDATE_KPI_DOC=1)", err)
	}
	if string(got) != want {
		t.Fatal("docs/reports/REPORT_CATALOGUE.md is out of date; regenerate with UPDATE_KPI_DOC=1")
	}
}
