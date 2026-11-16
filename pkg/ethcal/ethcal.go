// Package ethcal converts between the Ethiopian (Amete Mihret) and Gregorian
// calendars exactly, through the Julian Day Number.
//
// The Ethiopian year has twelve months of 30 days and a thirteenth month,
// Pagume, of 5 days (6 in the year before a Gregorian leap year: Ethiopian
// years divisible by 4 with remainder 3). Year 1 begins on JDN 1724221
// (29 August 8 CE, Julian calendar).
package ethcal

import (
	"errors"
	"fmt"
	"time"
)

// epoch is the JDN of the day before 1 Meskerem year 1 (Amete Mihret) minus 365:
// the constant of the classical algorithm (Dershowitz & Reingold).
const epoch = 1723856

// unixJDN is the JDN of 1 January 1970 (Gregorian).
const unixJDN = 2440588

// Month names in English transliteration, index 0 = Meskerem.
var Months = [13]string{"Meskerem", "Tikimt", "Hidar", "Tahsas", "Tir", "Yekatit", "Megabit", "Miyazya", "Ginbot", "Sene", "Hamle", "Nehase", "Pagume"}

// Date is an Ethiopian calendar date.
type Date struct {
	Year, Month, Day int
}

// ErrInvalid: no such Ethiopian date.
var ErrInvalid = errors.New("ethcal: no such Ethiopian date")

// IsLeap reports whether Ethiopian year y has a sixth day of Pagume.
func IsLeap(y int) bool { return y%4 == 3 }

// DaysIn returns the number of days of month m (1..13) of year y.
func DaysIn(y, m int) int {
	switch {
	case m >= 1 && m <= 12:
		return 30
	case m == 13 && IsLeap(y):
		return 6
	case m == 13:
		return 5
	}
	return 0
}

// Valid reports whether d is a real Ethiopian date (years 1 to 9999).
func (d Date) Valid() bool {
	return d.Year >= 1 && d.Year <= 9999 && d.Month >= 1 && d.Month <= 13 && d.Day >= 1 && d.Day <= DaysIn(d.Year, d.Month)
}

// JDN returns the Julian Day Number of d.
func (d Date) JDN() int {
	return (epoch + 365) + 365*(d.Year-1) + d.Year/4 + 30*d.Month + d.Day - 31
}

// FromJDN returns the Ethiopian date of a Julian Day Number.
func FromJDN(jdn int) Date {
	r := mod(jdn-epoch, 1461)
	n := mod(r, 365) + 365*(r/1460)
	y := 4*floorDiv(jdn-epoch, 1461) + r/365 - r/1460
	return Date{Year: y, Month: n/30 + 1, Day: mod(n, 30) + 1}
}

// FromGregorian converts a Gregorian date (its calendar day; time and zone are ignored).
func FromGregorian(t time.Time) Date {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return FromJDN(int(floorDiv64(day.Unix(), 86400)) + unixJDN)
}

// Gregorian converts d to the Gregorian calendar (UTC midnight).
func (d Date) Gregorian() (time.Time, error) {
	if !d.Valid() {
		return time.Time{}, fmt.Errorf("%w: %d-%02d-%02d", ErrInvalid, d.Year, d.Month, d.Day)
	}
	return time.Unix(int64(d.JDN()-unixJDN)*86400, 0).UTC(), nil
}

// String is the date as "6 Hidar 2019".
func (d Date) String() string {
	if d.Month < 1 || d.Month > 13 {
		return fmt.Sprintf("%d-%02d-%02d", d.Year, d.Month, d.Day)
	}
	return fmt.Sprintf("%d %s %d", d.Day, Months[d.Month-1], d.Year)
}

// ISO is the date as "2019-03-06" (Ethiopian year-month-day).
func (d Date) ISO() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day) }

// Parse reads an Ethiopian date written as YYYY-MM-DD.
func Parse(s string) (Date, error) {
	var d Date
	if _, err := fmt.Sscanf(s, "%4d-%2d-%2d", &d.Year, &d.Month, &d.Day); err != nil || len(s) != 10 || !d.Valid() {
		return Date{}, fmt.Errorf("%w: %q (write YYYY-MM-DD in the Ethiopian calendar)", ErrInvalid, s)
	}
	return d, nil
}

func mod(a, b int) int      { return ((a % b) + b) % b }
func floorDiv(a, b int) int { return (a - mod(a, b)) / b }
func floorDiv64(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}
