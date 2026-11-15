package fleet

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestFuelFlags(t *testing.T) {
	d := func(s string) decimal.Decimal { return decimal.RequireFromString(s) }
	std := func(s string) *decimal.Decimal { v := d(s); return &v }
	tol := DefaultThresholds()
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
		"everything agrees":            {FuelFigures{Dispensed: d("100"), Logged: d("100"), MeteredUnits: d("10"), LitresPerUnit: std("10")}, ""},
		"inside tolerance":             {FuelFigures{Dispensed: d("109"), Logged: d("100"), MeteredUnits: d("10"), LitresPerUnit: std("10")}, ""},
		"under a litre is never noise": {FuelFigures{Dispensed: d("1.90"), Logged: d("1.00"), MeteredUnits: d("0.2"), LitresPerUnit: std("10")}, ""},
		"dispensed but not logged":     {FuelFigures{Dispensed: d("150"), Logged: d("100"), MeteredUnits: d("15"), LitresPerUnit: std("10")}, "DISPENSED_NOT_LOGGED "},
		"logged but not dispensed":     {FuelFigures{Dispensed: d("100"), Logged: d("150"), MeteredUnits: d("10"), LitresPerUnit: std("10")}, "LOGGED_NOT_DISPENSED "},
		"above the standard":           {FuelFigures{Dispensed: d("150"), Logged: d("150"), MeteredUnits: d("10"), LitresPerUnit: std("10")}, "ABOVE_STANDARD "},
		"below the standard is fine":   {FuelFigures{Dispensed: d("60"), Logged: d("60"), MeteredUnits: d("10"), LitresPerUnit: std("10")}, ""},
		"fuel and no meter movement":   {FuelFigures{Dispensed: d("80"), Logged: d("80"), MeteredUnits: d("0"), LitresPerUnit: std("10")}, "FUEL_WITHOUT_METER_MOVEMENT "},
		"no standard":                  {FuelFigures{Dispensed: d("80"), Logged: d("80"), MeteredUnits: d("8"), LitresPerUnit: nil}, "NO_STANDARD "},
		"nothing at all":               {FuelFigures{Dispensed: d("0"), Logged: d("0"), MeteredUnits: d("0"), LitresPerUnit: nil}, ""},
		"two questions at once":        {FuelFigures{Dispensed: d("200"), Logged: d("100"), MeteredUnits: d("10"), LitresPerUnit: std("10")}, "DISPENSED_NOT_LOGGED ABOVE_STANDARD "},
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
	// A period that spans two standards: the expected figure is supplied log by log and takes precedence.
	split := FuelFigures{Dispensed: d("200"), Logged: d("200"), MeteredUnits: d("16"), LitresPerUnit: std("10"), ExpectedLitres: std("192")}
	if e := split.Expected(); e == nil || !e.Equal(d("192")) || codes(split) != "" {
		t.Errorf("expected across standards: %v, flags %q", e, codes(split))
	}
	// The thresholds are the policy's: a stricter policy lists what the default lets pass.
	strict := Thresholds{FuelTolerance: d("0.02"), FuelMinLitres: d("0"), DueSoonFraction: d("0.10"), DueSoonDays: 7}
	if f := FuelFlags(FuelFigures{Dispensed: d("105"), Logged: d("100"), MeteredUnits: d("10"), LitresPerUnit: std("10.5")}, strict); len(f) != 1 || f[0].Code != "DISPENSED_NOT_LOGGED" {
		t.Errorf("strict policy: %+v", f)
	}
	lax := Thresholds{FuelTolerance: d("0.10"), FuelMinLitres: d("100"), DueSoonFraction: d("0.10"), DueSoonDays: 7}
	if f := FuelFlags(FuelFigures{Dispensed: d("150"), Logged: d("100"), MeteredUnits: d("15"), LitresPerUnit: std("10")}, lax); len(f) != 0 {
		t.Errorf("a 50 L difference under a 100 L minimum was listed: %+v", f)
	}
}
