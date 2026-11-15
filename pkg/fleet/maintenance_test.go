package fleet

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestDueState(t *testing.T) {
	u := func(s string) *decimal.Decimal { v := decimal.RequireFromString(s); return &v }
	d := func(n int) *int { return &n }
	for name, c := range map[string]struct {
		unitsLeft, every *decimal.Decimal
		daysLeft, days   *int
		want             string
	}{
		"plenty of hours left":            {u("200"), u("250"), nil, nil, DueOK},
		"a tenth of the hours left":       {u("25"), u("250"), nil, nil, DueSoon},
		"just over a tenth":               {u("25.01"), u("250"), nil, nil, DueOK},
		"hours run out exactly":           {u("0"), u("250"), nil, nil, DueOverdue},
		"hours overrun":                   {u("-12"), u("250"), nil, nil, DueOverdue},
		"days only, far off":              {nil, nil, d(60), d(90), DueOK},
		"days only, a week left":          {nil, nil, d(7), d(90), DueSoon},
		"days only, a tenth of a year":    {nil, nil, d(36), d(365), DueSoon},
		"days only, today":                {nil, nil, d(0), d(90), DueOverdue},
		"both: hours fine, days overdue":  {u("200"), u("250"), d(-3), d(90), DueOverdue},
		"both: hours soon, days fine":     {u("10"), u("250"), d(60), d(90), DueSoon},
		"both: hours overdue, days soon":  {u("-1"), u("250"), d(5), d(90), DueOverdue},
		"no interval at all (never used)": {nil, nil, nil, nil, DueOK},
	} {
		if got := DueState(c.unitsLeft, c.every, c.daysLeft, c.days); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}
