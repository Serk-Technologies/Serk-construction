package fleet

import "github.com/shopspring/decimal"

// FuelFigures are the fuel facts of one machine in one period.
type FuelFigures struct {
	Dispensed     decimal.Decimal  // litres issued from stock into the machine (approved issues)
	Logged        decimal.Decimal  // litres written in the machine's approved daily logs
	MeteredUnits  decimal.Decimal  // hours or km run, from the approved logs' meter readings
	LitresPerUnit *decimal.Decimal // the standard in force; nil when none is set
}

// FuelFlag is one thing about a machine's fuel that a person should explain.
// A flag is a question, never a finding: it states two recorded figures that
// do not agree and by how much.
type FuelFlag struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// Expected is the litres the standard predicts for the metered units; nil without a standard.
func (f FuelFigures) Expected() *decimal.Decimal {
	if f.LitresPerUnit == nil {
		return nil
	}
	e := f.LitresPerUnit.Mul(f.MeteredUnits).Round(2)
	return &e
}

// FuelFlags compares dispensed, logged and expected fuel. tolerance is a
// fraction (0.10 = ten per cent) of the larger figure of each comparison;
// differences of less than one litre are never flagged.
func FuelFlags(f FuelFigures, tolerance decimal.Decimal) []FuelFlag {
	out := []FuelFlag{}
	one := decimal.NewFromInt(1)
	exceeds := func(a, b decimal.Decimal) bool { // a is more than b by over the tolerance
		d := a.Sub(b)
		return d.GreaterThan(one) && d.GreaterThan(decimal.Max(a, b).Mul(tolerance))
	}
	if exceeds(f.Dispensed, f.Logged) {
		out = append(out, FuelFlag{"DISPENSED_NOT_LOGGED", "Stock issues put " + f.Dispensed.StringFixed(2) + " L into this machine; its approved logs record " + f.Logged.StringFixed(2) + " L. The difference of " + f.Dispensed.Sub(f.Logged).StringFixed(2) + " L is to be explained (logs not yet approved, fuel not written in a log, or fuel that did not reach the machine)."})
	}
	if exceeds(f.Logged, f.Dispensed) {
		out = append(out, FuelFlag{"LOGGED_NOT_DISPENSED", "Approved logs record " + f.Logged.StringFixed(2) + " L; stock issues to this machine total " + f.Dispensed.StringFixed(2) + " L. The difference of " + f.Logged.Sub(f.Dispensed).StringFixed(2) + " L is to be explained (issues not yet approved, fuel from another source, or a logging error)."})
	}
	if f.Dispensed.IsPositive() && !f.MeteredUnits.IsPositive() {
		out = append(out, FuelFlag{"FUEL_WITHOUT_METER_MOVEMENT", "The machine took " + f.Dispensed.StringFixed(2) + " L but its approved logs show no meter movement in the period."})
	}
	if f.LitresPerUnit == nil {
		if f.Dispensed.IsPositive() || f.Logged.IsPositive() {
			out = append(out, FuelFlag{"NO_STANDARD", "No fuel standard is set for this machine, so its consumption cannot be compared with an expected figure."})
		}
		return out
	}
	if e := *f.Expected(); f.MeteredUnits.IsPositive() && exceeds(f.Dispensed, e) {
		out = append(out, FuelFlag{"ABOVE_STANDARD", "Dispensed " + f.Dispensed.StringFixed(2) + " L against " + e.StringFixed(2) + " L expected (" + f.MeteredUnits.StringFixed(2) + " units at " + f.LitresPerUnit.String() + " L per unit): " + f.Dispensed.Sub(e).StringFixed(2) + " L over. To be explained (working conditions, the machine's condition, the standard itself, or fuel that did not reach the machine)."})
	}
	return out
}
