package ethcal

import (
	"testing"
	"time"
)

// TestEthiopianCalendarRoundTrip (XCT-LOC.3): every Gregorian day from 1900
// to 2100 converts to a valid Ethiopian date and back to itself, consecutive
// days stay consecutive, and the new year falls where the independent rule
// says (11 September, or 12 September before a Gregorian leap year).
func TestEthiopianCalendarRoundTrip(t *testing.T) {
	start := time.Date(1900, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2100, 12, 31, 0, 0, 0, 0, time.UTC)
	prev := FromGregorian(start.AddDate(0, 0, -1))
	for g := start; !g.After(end); g = g.AddDate(0, 0, 1) {
		e := FromGregorian(g)
		if !e.Valid() {
			t.Fatalf("%s -> invalid %v", g.Format("2006-01-02"), e)
		}
		back, err := e.Gregorian()
		if err != nil || !back.Equal(g) {
			t.Fatalf("%s -> %v -> %s (%v)", g.Format("2006-01-02"), e, back.Format("2006-01-02"), err)
		}
		// Consecutive: the next day of prev.
		want := Date{prev.Year, prev.Month, prev.Day + 1}
		if want.Day > DaysIn(want.Year, want.Month) {
			want = Date{want.Year, want.Month + 1, 1}
			if want.Month > 13 {
				want = Date{want.Year + 1, 1, 1}
			}
		}
		if e != want {
			t.Fatalf("%s is %v, expected the day after %v", g.Format("2006-01-02"), e, prev)
		}
		prev = e
	}
	for y := 1901; y <= 2099; y++ {
		d := 11
		if y%4 == 3 && (y+1)%4 == 0 { // the Gregorian year after has 29 February (1901-2099)
			d = 12
		}
		ny := time.Date(y, 9, d, 0, 0, 0, 0, time.UTC)
		if got := FromGregorian(ny); got != (Date{y - 7, 1, 1}) {
			t.Fatalf("new year %s is %v, want 1 Meskerem %d", ny.Format("2006-01-02"), got, y-7)
		}
	}
}

// TestEthiopianCalendarKnownDates checks dates published in the calendar.
func TestEthiopianCalendarKnownDates(t *testing.T) {
	for _, c := range []struct {
		g string
		e Date
	}{
		{"2024-09-11", Date{2017, 1, 1}},  // Enkutatash 2017
		{"2023-09-12", Date{2016, 1, 1}},  // Enkutatash 2016 (before Gregorian 2024, a leap year)
		{"2025-01-07", Date{2017, 4, 29}}, // Genna (Christmas), 29 Tahsas
		{"2023-09-11", Date{2015, 13, 6}}, // Pagume 6 of 2015 (a leap year)
		{"2026-11-15", Date{2019, 3, 6}},
	} {
		g, _ := time.Parse("2006-01-02", c.g)
		if got := FromGregorian(g); got != c.e {
			t.Errorf("%s: got %v, want %v", c.g, got, c.e)
		}
	}
	for _, bad := range []string{"2016-13-06", "2015-13-07", "2017-00-10", "2017-02-31", "17-02-01", "2017-2-1"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse accepted %q", bad)
		}
	}
	if d, err := Parse("2015-13-06"); err != nil || d != (Date{2015, 13, 6}) {
		t.Errorf("Parse(2015-13-06) = %v, %v", d, err)
	}
}
