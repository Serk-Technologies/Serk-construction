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
// when either interval has run out, DUE_SOON when a tenth or less of an
// interval is left (and for days, also when seven days or fewer are left),
// and OK otherwise. nil means the plan does not use that interval.
func DueState(unitsLeft, everyUnits *decimal.Decimal, daysLeft, everyDays *int) string {
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
		case unitsLeft.LessThanOrEqual(everyUnits.Div(decimal.NewFromInt(10))):
			worse(DueSoon)
		}
	}
	if daysLeft != nil && everyDays != nil {
		switch {
		case *daysLeft <= 0:
			worse(DueOverdue)
		case *daysLeft <= 7 || *daysLeft*10 <= *everyDays:
			worse(DueSoon)
		}
	}
	return state
}
