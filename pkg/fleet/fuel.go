package fleet

import "github.com/shopspring/decimal"

// FuelFigures are the fuel facts of one machine in one period.
type FuelFigures struct {
	Dispensed     decimal.Decimal  // litres issued from stock into the machine (approved issues)
	Logged        decimal.Decimal  // litres written in the machine's approved daily logs
	MeteredUnits  decimal.Decimal  // hours or km run, from the approved logs' meter readings
	LitresPerUnit *decimal.Decimal // the standard in force at the end of the period; nil when none is set
	// ExpectedLitres, when set, is the expected fuel computed log by log at the
	// standard in force on each log's date (a period may span several
	// standards). It takes precedence over MeteredUnits x LitresPerUnit.
	ExpectedLitres *decimal.Decimal
}

// Thresholds is the part of the approved fleet policy that the comparisons use.
type Thresholds struct {
	FuelTolerance   decimal.Decimal // share of the larger figure (0.10 = ten per cent)
	FuelMinLitres   decimal.Decimal // differences up to this many litres are never listed
	DueSoonFraction decimal.Decimal // share of a service interval left at which it is due soon
	DueSoonDays     int             // days left at which it is due soon
}

// DefaultThresholds are engineering defaults used only while a tenant has no
// approved fleet policy. They are not a policy; reports that use them say so.
func DefaultThresholds() Thresholds {
	return Thresholds{FuelTolerance: decimal.RequireFromString("0.10"), FuelMinLitres: decimal.NewFromInt(1), DueSoonFraction: decimal.RequireFromString("0.10"), DueSoonDays: 7}
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
	if f.ExpectedLitres != nil {
		e := f.ExpectedLitres.Round(2)
		return &e
	}
	if f.LitresPerUnit == nil {
		return nil
	}
	e := f.LitresPerUnit.Mul(f.MeteredUnits).Round(2)
	return &e
}

// FuelFlags compares dispensed, logged and expected fuel against the policy's
// thresholds: a difference is listed when it exceeds both the tolerance (a
// share of the larger figure) and the minimum litres.
func FuelFlags(f FuelFigures, t Thresholds) []FuelFlag {
	out := []FuelFlag{}
	exceeds := func(a, b decimal.Decimal) bool { // a is more than b by over the thresholds
		d := a.Sub(b)
		return d.GreaterThan(t.FuelMinLitres) && d.GreaterThan(decimal.Max(a, b).Mul(t.FuelTolerance))
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
	if f.Expected() == nil {
		if f.Dispensed.IsPositive() || f.Logged.IsPositive() {
			out = append(out, FuelFlag{"NO_STANDARD", "No fuel standard was in force for this machine on the days it was logged, so its consumption cannot be compared with an expected figure."})
		}
		return out
	}
	if e := *f.Expected(); f.MeteredUnits.IsPositive() && exceeds(f.Dispensed, e) {
		out = append(out, FuelFlag{"ABOVE_STANDARD", "Dispensed " + f.Dispensed.StringFixed(2) + " L against " + e.StringFixed(2) + " L expected for " + f.MeteredUnits.StringFixed(2) + " metered units at the standard in force on each day: " + f.Dispensed.Sub(e).StringFixed(2) + " L over. To be explained (working conditions, the machine's condition, the standard itself, or fuel that did not reach the machine)."})
	}
	return out
}
