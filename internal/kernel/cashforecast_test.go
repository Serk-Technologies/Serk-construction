package kernel

// Tests for cash forecasting (WP-2.12, migration 0059, kernel/cashforecast.go).

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/serk-group/serk-construction/pkg/auth"
)

func TestCashForecast_ThirteenWeeks(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	// An approved purchase order partly invoiced: the invoice is a confirmed
	// outflow, the rest of the order a forecast one.
	_, _, po := w.awardedPO(t, "20", "500")
	_, _ = w.receipt(t, po, GRNLine{POLineNo: 1, QtyReceived: dec("12")}, GRNLine{POLineNo: 2, QtyReceived: dec("500")})
	inv, _ := w.invoice(t, InvoiceInput{POID: po, InvoiceNumber: "CF/2026/1", InvoiceDate: today(), DueDate: today().AddDate(0, 0, 20),
		Subtotal: dec("1486500"), TaxAmount: dec("222975"), Total: dec("1709475"), Lines: []InvoiceLine{{1, dec("12"), dec("84500")}, {2, dec("500"), dec("945")}}})
	_, err := f.svc.ApproveInvoice(f.ctx, c.apSup, inv, "")
	must(t, "approve invoice", err)
	// A certified client certificate not invoiced, and one invoiced.
	w.activeContract(t)
	w.measure(t, 15, "4000", "300", "20")
	_, _ = w.approvedInvoice(t, w.certifiedIPC(t, 16, "CF/IPC/1"))
	w.measure(t, 20, "3000", "200", "10")
	w.certifiedIPC(t, 20, "CF/IPC/2")

	if _, err := f.svc.AddCashPlanItem(f.ctx, w.ar.qs, CashPlanInput{LegalEntityID: f.entity, DueOn: today().AddDate(0, 0, 14).Format("2006-01-02"), Direction: "OUT", Category: "TAX", Amount: dec("1"), Description: "VAT"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a QS added a planned cash item: %v", err)
	}
	loan, err := f.svc.AddCashPlanItem(f.ctx, c.treasury, CashPlanInput{LegalEntityID: f.entity, DueOn: today().AddDate(0, 0, 10).Format("2006-01-02"), Direction: "in", Category: "loan", Amount: dec("5000000"), Description: "Working capital loan drawdown", Confidence: "CONFIRMED"})
	must(t, "loan", err)
	_, err = f.svc.AddCashPlanItem(f.ctx, c.treasury, CashPlanInput{LegalEntityID: f.entity, DueOn: today().AddDate(0, 0, 200).Format("2006-01-02"), Direction: "OUT", Category: "LOAN", Amount: dec("1000000"), Description: "First repayment"})
	must(t, "repayment", err)

	fc, err := f.svc.CashForecastOf(f.ctx, c.cfo, f.entity)
	must(t, "forecast", err)
	if len(fc.Weeks) != 13 {
		t.Fatalf("weeks: %d", len(fc.Weeks))
	}
	sum := map[string]decimal.Decimal{}
	sources := map[string]bool{}
	var inside, later decimal.Decimal
	for _, it := range fc.Items {
		sum[it.Direction+"/"+it.Confidence] = sum[it.Direction+"/"+it.Confidence].Add(it.Amount)
		sources[it.Source] = true
		v := it.Amount
		if it.Direction == "OUT" {
			v = v.Neg()
		}
		if it.Week < 0 {
			later = later.Add(v)
		} else {
			inside = inside.Add(v)
		}
	}
	for _, s := range []string{"Customer invoice", "Supplier invoice", "Client certificate not yet invoiced", "Purchase order not yet invoiced", "Planned: loan"} {
		if !sources[s] {
			t.Fatalf("no item from %q: %+v", s, fc.Items)
		}
	}
	if !fc.Weeks[12].Closing.Equal(fc.Opening.Add(inside)) || !fc.Later.Equal(later) || !later.Equal(dec("-1000000")) {
		t.Fatalf("running balance: opening %s, inside %s, closing %s, later %s", fc.Opening, inside, fc.Weeks[12].Closing, fc.Later)
	}
	var weeksIn decimal.Decimal
	for _, wk := range fc.Weeks {
		weeksIn = weeksIn.Add(wk.InConfirmed)
	}
	if !weeksIn.Equal(sum["IN/CONFIRMED"]) {
		t.Fatalf("confirmed inflows by week %s, by item %s", weeksIn, sum["IN/CONFIRMED"])
	}
	if poOpen := sum["OUT/FORECAST"]; !poOpen.IsPositive() {
		t.Fatalf("no forecast outflow for the open order: %v", sum)
	}

	// Cancelling a planned item takes it out; the history stays.
	if err := f.svc.CancelCashPlanItem(f.ctx, c.treasury, loan, ""); err == nil {
		t.Fatal("a planned item was cancelled without a reason")
	}
	must(t, "cancel", f.svc.CancelCashPlanItem(f.ctx, c.treasury, loan, "Loan approval postponed"))
	fc2, err := f.svc.CashForecastOf(f.ctx, c.cfo, f.entity)
	must(t, "forecast 2", err)
	if !fc2.Weeks[12].Closing.Equal(fc.Weeks[12].Closing.Sub(dec("5000000"))) || len(fc2.Plan) != 2 {
		t.Fatalf("after cancelling the loan: %s (was %s), plan %d", fc2.Weeks[12].Closing, fc.Weeks[12].Closing, len(fc2.Plan))
	}
	rawFails(t, f, "edit a planned item", `UPDATE cash_plan_items SET status = 'ACTIVE', cancel_reason = NULL WHERE id = '`+loan+`'`)
	if _, err := f.svc.CashForecastOf(f.ctx, w.ar.qs, f.entity); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a project-scoped QS read the cash forecast: %v", err)
	}
}
