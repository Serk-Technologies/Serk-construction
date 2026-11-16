package kernel

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// TestForecast_ChangeRuleNeedsExplanation (DEF-019; 0058): a line whose EAC
// moved by more than 10% of its budget since the last approved forecast, while
// staying within the budget, is refused at submission until its note explains
// the change; a move of 10% or less needs no explanation. Driven with MANUAL
// estimates so the move is chosen exactly, not left to the data.
func TestForecast_ChangeRuleNeedsExplanation(t *testing.T) {
	w := newARWorld(t)
	f, c := w.f, w.cast
	period := func(daysAgo int) string { return today().AddDate(0, 0, -daysAgo).Format("2006-01-02") } // one approved forecast per period

	f1, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, period(60), "")
	must(t, "forecast 1", err)
	fc, err := f.svc.GetForecast(f.ctx, c.pm, f1)
	must(t, "get 1", err)
	l := fc.Lines[0]
	if !l.Budget.IsPositive() {
		t.Fatalf("the first line has no budget: %+v", l)
	}
	// EAC = cost to date + ETC; aim at 90% of the budget, then 75% (a 15% move) and 70% (5% more).
	etcFor := func(share string) *decimal.Decimal {
		x := l.Budget.Mul(dec(share)).Sub(l.CostToDate).Round(2)
		if x.IsNegative() {
			t.Fatalf("cost to date %s already exceeds %s of the budget %s", l.CostToDate, share, l.Budget)
		}
		return &x
	}
	must(t, "line 1", f.svc.SetForecastLine(f.ctx, c.pm, f1, l.CostCodeID, "MANUAL", etcFor("0.90"), "Initial estimate from the site team"))
	must(t, "submit 1", f.svc.SubmitForecast(f.ctx, c.pm, f1))
	must(t, "approve 1", f.svc.DecideForecast(f.ctx, c.cfo, f1, true, ""))

	f2, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, period(30), "")
	must(t, "forecast 2", err)
	must(t, "line 2", f.svc.SetForecastLine(f.ctx, c.pm, f2, l.CostCodeID, "MANUAL", etcFor("0.75"), ""))
	err = f.svc.SubmitForecast(f.ctx, c.pm, f2)
	if err == nil || !strings.Contains(err.Error(), "moved by more than 10%") {
		t.Fatalf("a 15%% move without an explanation was submitted: %v", err)
	}
	must(t, "explain", f.svc.SetForecastLine(f.ctx, c.pm, f2, l.CostCodeID, "MANUAL", etcFor("0.75"), "Rock excavation avoided by the redesign"))
	must(t, "submit 2", f.svc.SubmitForecast(f.ctx, c.pm, f2))
	must(t, "approve 2", f.svc.DecideForecast(f.ctx, c.cfo, f2, true, ""))

	f3, err := f.svc.CreateForecast(f.ctx, c.pm, f.project, period(0), "")
	must(t, "forecast 3", err)
	must(t, "line 3", f.svc.SetForecastLine(f.ctx, c.pm, f3, l.CostCodeID, "MANUAL", etcFor("0.70"), ""))
	must(t, "a 5% move needs no explanation", f.svc.SubmitForecast(f.ctx, c.pm, f3))
}
