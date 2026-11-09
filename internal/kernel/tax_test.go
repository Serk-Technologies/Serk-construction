package kernel

// Tests for tax returns and e-invoicing (WP-2.11, migration 0063, kernel/tax.go).
// A VAT return nets output VAT (from an approved customer invoice) against
// input VAT (from an approved supplier invoice: 222,975); both journals are
// posted. The return's lines are the database's computation of posted journals.

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

func TestTax_VATReturnAndEInvoice(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	acc, cfo := f.prep, f.cfo
	must(t, "wht account", f.db.AdminStore.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		id, err := postgres.CreateAccount(ctx, tx, ledger.Account{TenantID: f.tenant, LegalEntityID: f.entity, Code: "2330", Name: "Withholding tax payable", Type: ledger.Liability, Active: true})
		w.acct["2330"] = id
		return err
	}))

	// Posted VAT: input from a supplier invoice, output from a client invoice.
	_, _, po := w.awardedPO(t, "20", "500")
	_, _ = w.receipt(t, po, GRNLine{POLineNo: 1, QtyReceived: dec("12")}, GRNLine{POLineNo: 2, QtyReceived: dec("500")})
	inv, _ := w.invoice(t, InvoiceInput{POID: po, InvoiceNumber: "TX/2026/1", InvoiceDate: today(), DueDate: today().AddDate(0, 0, 20),
		Subtotal: dec("1486500"), TaxAmount: dec("222975"), Total: dec("1709475"), Lines: []InvoiceLine{{1, dec("12"), dec("84500")}, {2, dec("500"), dec("945")}}})
	apJ, err := f.svc.ApproveInvoice(f.ctx, c.apSup, inv, "")
	must(t, "approve supplier invoice", err)
	w.activeContract(t)
	w.measure(t, 15, "4000", "300", "20")
	arInv, arJ := w.approvedInvoice(t, w.certifiedIPC(t, 16, "TX/IPC/1"))
	w.financePost(t, apJ, arJ)
	var outVAT string
	must(t, "invoice vat", f.app.WithTenant(f.ctx, f.tenant, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT vat_amount::text FROM customer_invoices WHERE id = $1::uuid`, arInv).Scan(&outVAT)
	}))

	// Designation: wrong type refused; the accountant prepares, a QS cannot.
	if _, err := f.svc.DesignateTaxAccount(f.ctx, acc, w.acct["1300"], "OUTPUT_VAT"); err == nil {
		t.Fatal("an asset account was designated as output VAT")
	}
	if _, err := f.svc.DesignateTaxAccount(f.ctx, w.ar.qs, w.acct["2300"], "OUTPUT_VAT"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a QS designated a tax account: %v", err)
	}
	month := today().Format("2006-01")
	start := month + "-01"
	var end string
	end = time.Date(today().Year(), today().Month()+1, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	if _, err := f.svc.PrepareTaxReturn(f.ctx, acc, f.entity, "VAT", start, end); err == nil {
		t.Fatal("a return before any tax account is designated")
	}
	for _, d := range [][2]string{{"2300", "OUTPUT_VAT"}, {"1300", "INPUT_VAT"}, {"2330", "WHT_PAYABLE"}} {
		_, err := f.svc.DesignateTaxAccount(f.ctx, acc, w.acct[d[0]], d[1])
		must(t, "designate "+d[0], err)
	}

	ret, err := f.svc.PrepareTaxReturn(f.ctx, acc, f.entity, "VAT", start, end)
	must(t, "prepare", err)
	if _, err := f.svc.PrepareTaxReturn(f.ctx, acc, f.entity, "VAT", start, end); err == nil {
		t.Fatal("a second live return for the same period")
	}
	rawFails(t, f, "forge a return line", `INSERT INTO tax_return_lines (return_id, account_code, account_name, tax_kind, amount) VALUES ('`+ret+`', 'X', 'X', 'OUTPUT_VAT', 1)`)
	v, err := f.svc.TaxView(f.ctx, acc, ret)
	must(t, "view", err)
	got := map[string]string{}
	for _, l := range v["lines"].([]map[string]any) {
		got[l["tax_kind"].(string)] = l["amount"].(interface{ String() string }).String()
	}
	if !dec(got["OUTPUT_VAT"]).Equal(dec(outVAT)) || !dec(got["INPUT_VAT"]).Equal(dec("-222975")) {
		t.Fatalf("return lines: %v (client VAT %s)", got, outVAT)
	}
	if _, err := f.svc.TaxView(f.ctx, w.ar.qs, ""); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a QS read the tax page: %v", err)
	}

	// A filing needs approval; the preparer cannot approve; MFA is fresh.
	if err := f.svc.FileTaxReturn(f.ctx, acc, ret, "ERCA-REF-1"); err == nil {
		t.Fatal("an unapproved return was filed")
	}
	selfCFO := cfo
	selfCFO.UserID = acc.UserID
	if err := f.svc.DecideTaxReturn(f.ctx, selfCFO, ret, true, ""); !errors.Is(err, auth.ErrSoD) {
		t.Fatalf("the preparer approved their own return: %v", err)
	}
	stale := cfo
	stale.AuthTime = time.Now().Add(-time.Hour)
	if err := f.svc.DecideTaxReturn(f.ctx, stale, ret, true, ""); err == nil {
		t.Fatal("approved without fresh MFA")
	}
	must(t, "approve", f.svc.DecideTaxReturn(f.ctx, cfo, ret, true, ""))
	if err := f.svc.FileTaxReturn(f.ctx, acc, ret, "x"); err == nil {
		t.Fatal("filed without the authority's reference")
	}
	must(t, "file", f.svc.FileTaxReturn(f.ctx, acc, ret, "ERCA-REF-1"))
	rawFails(t, f, "edit a filed return", `UPDATE tax_returns SET filing_ref = 'changed' WHERE id = '`+ret+`'`)
	if err := f.svc.CancelTaxReturn(f.ctx, acc, ret, "Too late to cancel"); err == nil {
		t.Fatal("a filed return was cancelled")
	}

	// A withholding return (nothing withheld yet) is prepared, rejected, cancelled.
	wht, err := f.svc.PrepareTaxReturn(f.ctx, acc, f.entity, "WHT", start, end)
	must(t, "wht", err)
	must(t, "reject", f.svc.DecideTaxReturn(f.ctx, cfo, wht, false, "Wrong period for this return"))

	// E-invoice: the fake adapter accepts an approved invoice once.
	res, err := f.svc.SubmitEInvoice(f.ctx, acc, arInv)
	must(t, "e-invoice", err)
	if res["status"] != "ACCEPTED" || res["reference"] == "" {
		t.Fatalf("e-invoice: %v", res)
	}
	if _, err := f.svc.SubmitEInvoice(f.ctx, acc, arInv); err == nil {
		t.Fatal("an invoice was accepted twice")
	}
	if _, err := f.svc.SubmitEInvoice(f.ctx, w.ar.qs, arInv); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a QS submitted an e-invoice: %v", err)
	}
	if ref, ok, _, _ := (FakeEInvoice{}).Submit(f.ctx, EInvoice{Number: "X"}, "aaaaaaaaaaaaaaaaaaaa"); ok || ref != "" {
		t.Fatal("the fake accepted an invoice without a customer TIN")
	}
}
