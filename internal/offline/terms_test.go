package offline

import (
	"testing"
)

func TestCalculateCalendarYearsAndMonthEnd(t *testing.T) {
	for _, test := range []struct {
		name, base, kind, expected string
	}{
		{"month-end", "2027-01-31T15:30:00.000Z", "months", "2027-02-28T15:30:00.000Z"},
		{"leap-day-year", "2028-02-29T15:30:00.000Z", "years", "2029-02-28T15:30:00.000Z"},
		{"calendar-year", "2026-03-01T15:30:00.000Z", "years", "2027-03-01T15:30:00.000Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, err := ParseUTC(test.base)
			if err != nil {
				t.Fatal(err)
			}
			result, err := CalculateTerm(base, "initial", Term{Kind: test.kind, Amount: 1}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if result.ExpiresAt == nil || *result.ExpiresAt != test.expected {
				t.Fatalf("got %v, want %s", result.ExpiresAt, test.expected)
			}
		})
	}
}

func TestCalculateTermAcceptsEffectiveDateAtBeijingMidnightToday(t *testing.T) {
	base, err := ParseUTC("2026-10-29T04:00:00.000Z") // 12:00 in Beijing
	if err != nil {
		t.Fatal(err)
	}
	result, err := CalculateTerm(base, "initial", Term{Kind: "days", Amount: 30, StartsAt: "2026-10-29"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NotBefore != "2026-10-28T16:00:00.000Z" {
		t.Fatalf("got %s, want Beijing midnight converted to UTC", result.NotBefore)
	}
	if _, err = CalculateTerm(base, "initial", Term{Kind: "days", Amount: 30, StartsAt: "2026-10-28"}, nil); err == nil {
		t.Fatal("accepted an effective date before today")
	}
}

func TestCalculateTermRejectsDatesBeyondSupportedYear(t *testing.T) {
	base, err := ParseUTC("9998-12-31T15:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range []Term{{Kind: "years", Amount: 2}, {Kind: "months", Amount: 13}, {Kind: "days", Amount: 400}} {
		if _, err = CalculateTerm(base, "initial", term, nil); err == nil {
			t.Fatalf("accepted out-of-range term: %+v", term)
		}
	}
}
