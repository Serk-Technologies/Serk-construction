package fleet

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestFuelFlags(t *testing.T) {
	d := func(s string) decimal.Decimal { return decimal.RequireFromString(s) }
	std := func(s string) *decimal.Decimal { v := d(s); return &v }
	tol := d("0.10")
	codes := func(f FuelFigures) string {
		s := ""
		for _, x := range FuelFlags(f, tol) {
			if x.Reason == "" {
				t.Fatalf("flag %s has no reason", x.Code)
			}
			s += x.Code + " "
		}
		return s
	}
	for name, c := range map[string]struct {
		f    FuelFigures
		want string
	}{
		"everything agrees":            {FuelFigures{d("100"), d("100"), d("10"), std("10")}, ""},
		"inside tolerance":             {FuelFigures{d("109"), d("100"), d("10"), std("10")}, ""},
		"under a litre is never noise": {FuelFigures{d("1.90"), d("1.00"), d("0.2"), std("10")}, ""},
		"dispensed but not logged":     {FuelFigures{d("150"), d("100"), d("15"), std("10")}, "DISPENSED_NOT_LOGGED "},
		"logged but not dispensed":     {FuelFigures{d("100"), d("150"), d("10"), std("10")}, "LOGGED_NOT_DISPENSED "},
		"above the standard":           {FuelFigures{d("150"), d("150"), d("10"), std("10")}, "ABOVE_STANDARD "},
		"below the standard is fine":   {FuelFigures{d("60"), d("60"), d("10"), std("10")}, ""},
		"fuel and no meter movement":   {FuelFigures{d("80"), d("80"), d("0"), std("10")}, "FUEL_WITHOUT_METER_MOVEMENT "},
		"no standard":                  {FuelFigures{d("80"), d("80"), d("8"), nil}, "NO_STANDARD "},
		"nothing at all":               {FuelFigures{d("0"), d("0"), d("0"), nil}, ""},
		"two questions at once":        {FuelFigures{d("200"), d("100"), d("10"), std("10")}, "DISPENSED_NOT_LOGGED ABOVE_STANDARD "},
	} {
		if got := codes(c.f); got != c.want {
			t.Errorf("%s: flags %q, want %q", name, got, c.want)
		}
	}
	if e := (FuelFigures{MeteredUnits: d("12.5"), LitresPerUnit: std("8.4")}).Expected(); e == nil || !e.Equal(d("105")) {
		t.Errorf("expected litres: %v", e)
	}
	if e := (FuelFigures{MeteredUnits: d("12.5")}).Expected(); e != nil {
		t.Errorf("expected without a standard: %v", e)
	}
}
