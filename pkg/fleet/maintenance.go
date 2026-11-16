package fleet

import "github.com/shopspring/decimal"

// Due states of a preventive maintenance plan.
const (
	DueOK      = "OK"
	DueSoon    = "DUE_SOON"
	DueOverdue = "OVERDUE"
)

// DueState says how close a plan is to its next service. A plan can run on
// metered units, on days, or on both (whichever comes first). It is OVERDUE
// when either interval has run out, DUE_SOON when the policy's share of an
// interval or less is left (and for days, also when the policy's number of
// days or fewer are left), and OK otherwise. nil means the plan does not use
// that interval.
func DueState(unitsLeft, everyUnits *decimal.Decimal, daysLeft, everyDays *int, t Thresholds) string {
	state := DueOK
	worse := func(s string) {
		if s == DueOverdue || (s == DueSoon && state == DueOK) {
			state = s
		}
	}
	if unitsLeft != nil && everyUnits != nil {
		switch {
		case !unitsLeft.IsPositive():
			worse(DueOverdue)
		case unitsLeft.LessThanOrEqual(everyUnits.Mul(t.DueSoonFraction)):
			worse(DueSoon)
		}
	}
	if daysLeft != nil && everyDays != nil {
		switch {
		case *daysLeft <= 0:
			worse(DueOverdue)
		case *daysLeft <= t.DueSoonDays || decimal.NewFromInt(int64(*daysLeft)).LessThanOrEqual(decimal.NewFromInt(int64(*everyDays)).Mul(t.DueSoonFraction)):
			worse(DueSoon)
		}
	}
	return state
}
