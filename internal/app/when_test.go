package app

import (
	"testing"
	"time"
)

func TestParseWhen(t *testing.T) {
	// Monday 26 Oct 2026, 14:30
	now := time.Date(2026, 10, 26, 14, 30, 0, 0, time.Local)
	at := func(mo time.Month, d, h, mi int) time.Time { return time.Date(2026, mo, d, h, mi, 0, 0, time.Local) }
	for in, want := range map[string]time.Time{
		"in 10m":             at(10, 26, 14, 40),
		"in 2h":              at(10, 26, 16, 30),
		"2 hours":            at(10, 26, 16, 30),
		"in 3 days":          at(10, 29, 14, 30),
		"tomorrow":           at(10, 27, 9, 0),
		"tomorrow 9am":       at(10, 27, 9, 0),
		"tomorrow at 6:30pm": at(10, 27, 18, 30),
		"tmrw 18:00":         at(10, 27, 18, 0),
		"6pm":                at(10, 26, 18, 0),
		"9am":                at(10, 27, 9, 0), // passed today: tomorrow
		"18.15":              at(10, 26, 18, 15),
		"tonight":            at(10, 26, 21, 0),
		"evening":            at(10, 26, 18, 0),
		"tomorrow morning":   at(10, 27, 9, 0),
		"fri":                at(10, 30, 9, 0),
		"friday 4pm":         at(10, 30, 16, 0),
		"next mon 10:00":     at(11, 2, 10, 0), // today is Monday: the next one
		"3 nov":              at(11, 3, 9, 0),
		"nov 3 10:30":        at(11, 3, 10, 30),
		"03/11 9am":          at(11, 3, 9, 0),
		"2026-12-01 18:00":   at(12, 1, 18, 0),
		"5 jan":              time.Date(2027, 1, 5, 9, 0, 0, 0, time.Local), // next year
		"12 pm":              at(10, 27, 12, 0),
	} {
		got, err := ParseWhen(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: got %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "yesterday", "today 9am", "banana", "25:00", "in 0m"} {
		if _, err := ParseWhen(bad, now); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
	if s := SendTime(at(10, 27, 9, 0), now); s != "tomorrow 09:00" {
		t.Errorf("SendTime: %q", s)
	}
}
