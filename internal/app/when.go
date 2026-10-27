package app

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseWhen reads a time to send at, the way people write it:
//
//	in 10m, in 2h, in 3 days        tomorrow, tomorrow 9am, tomorrow 18:30
//	9am, 6:30pm, 18:00 (today, or tomorrow once it's past)
//	mon, friday 4pm, next tue 10:00   tonight, this evening, morning, noon
//	3 nov, nov 3 10:30, 03/11 9am, 2026-11-03 18:00
//
// A day without a time means 9:00. The result is always in the future.
func ParseWhen(s string, now time.Time) (time.Time, error) {
	in := strings.ToLower(strings.TrimSpace(s))
	in = strings.Join(strings.Fields(strings.ReplaceAll(in, ",", " ")), " ")
	if in == "" {
		return time.Time{}, fmt.Errorf("when? e.g. tomorrow 9am, fri 6pm, in 2h")
	}
	// in N units
	if m := reIn.FindStringSubmatch(in); m != nil {
		n, _ := strconv.Atoi(m[1])
		var d time.Duration
		switch m[2][0] {
		case 'm':
			d = time.Duration(n) * time.Minute
		case 'h':
			d = time.Duration(n) * time.Hour
		case 'd':
			d = time.Duration(n) * 24 * time.Hour
		case 'w':
			d = time.Duration(n) * 7 * 24 * time.Hour
		}
		if d <= 0 {
			return time.Time{}, fmt.Errorf("%q is not in the future", s)
		}
		return now.Add(d).Truncate(time.Minute), nil
	}
	words := strings.Fields(in)
	day := time.Time{} // the date, when one was given
	var hour, minute = -1, 0
	for i := 0; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "at" || w == "on" || w == "next" || w == "this":
		case w == "today":
			day = midnight(now)
		case w == "tomorrow" || w == "tmrw" || w == "tom":
			day = midnight(now).AddDate(0, 0, 1)
		case w == "morning":
			hour, minute = 9, 0
		case w == "noon" || w == "midday":
			hour, minute = 12, 0
		case w == "afternoon":
			hour, minute = 14, 0
		case w == "evening":
			hour, minute = 18, 0
		case w == "tonight" || w == "night":
			hour, minute = 21, 0
			if day.IsZero() {
				day = midnight(now)
			}
		case weekday(w) >= 0:
			wd := time.Weekday(weekday(w))
			d := midnight(now)
			for d.Weekday() != wd || !d.After(midnight(now)) {
				d = d.AddDate(0, 0, 1)
			}
			day = d
		case reClock.MatchString(w) && !(i+1 < len(words) && month(words[i+1]) > 0 && !strings.ContainsAny(w, ":.apm")):
			m := reClock.FindStringSubmatch(w)
			h, _ := strconv.Atoi(m[1])
			mi := 0
			if m[2] != "" {
				mi, _ = strconv.Atoi(m[2])
			}
			ampm := m[3]
			if ampm == "" && i+1 < len(words) && (words[i+1] == "am" || words[i+1] == "pm") {
				ampm = words[i+1]
				i++
			}
			switch {
			case ampm == "pm" && h < 12:
				h += 12
			case ampm == "am" && h == 12:
				h = 0
			}
			if h > 23 || mi > 59 {
				return time.Time{}, fmt.Errorf("%q is not a time of day", w)
			}
			hour, minute = h, mi
		case reISO.MatchString(w):
			t, err := time.ParseInLocation("2006-01-02", w, now.Location())
			if err != nil {
				return time.Time{}, err
			}
			day = t
		case reDM.MatchString(w): // 03/11 or 3/11/2026: day first
			m := reDM.FindStringSubmatch(w)
			d, _ := strconv.Atoi(m[1])
			mo, _ := strconv.Atoi(m[2])
			y := now.Year()
			if m[3] != "" {
				y, _ = strconv.Atoi(m[3])
				if y < 100 {
					y += 2000
				}
			}
			day = time.Date(y, time.Month(mo), d, 0, 0, 0, 0, now.Location())
			if m[3] == "" && day.Before(midnight(now)) {
				day = day.AddDate(1, 0, 0)
			}
		case month(w) > 0:
			// "nov 3" or "3 nov"
			mo := month(w)
			d := 0
			if i+1 < len(words) {
				if n, err := strconv.Atoi(strings.TrimRight(words[i+1], "stndrh")); err == nil && n >= 1 && n <= 31 {
					d = n
					i++
				}
			}
			if d == 0 && i > 0 {
				if n, err := strconv.Atoi(strings.TrimRight(words[i-1], "stndrh")); err == nil {
					d = n
				}
			}
			if d == 0 {
				return time.Time{}, fmt.Errorf("which day of %s?", w)
			}
			day = time.Date(now.Year(), time.Month(mo), d, 0, 0, 0, 0, now.Location())
			if day.Before(midnight(now)) {
				day = day.AddDate(1, 0, 0)
			}
		default:
			if n, err := strconv.Atoi(strings.TrimRight(w, "stndrh")); err == nil && n >= 1 && n <= 31 && i+1 < len(words) && month(words[i+1]) > 0 {
				continue // "3 nov": the month reads it
			}
			return time.Time{}, fmt.Errorf("couldn't read %q (try: tomorrow 9am, fri 6pm, in 2h, 3 nov 10:30)", w)
		}
	}
	if day.IsZero() && hour < 0 {
		return time.Time{}, fmt.Errorf("couldn't read %q", s)
	}
	if hour < 0 {
		hour = 9
	}
	if day.IsZero() {
		day = midnight(now)
		if !time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, now.Location()).After(now) {
			day = day.AddDate(0, 0, 1) // that time has passed today: tomorrow
		}
	}
	t := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, now.Location())
	if !t.After(now) {
		return time.Time{}, fmt.Errorf("%s has already passed", t.Format("Mon 02 Jan 15:04"))
	}
	return t, nil
}

var (
	reIn    = regexp.MustCompile(`^(?:in )?(\d+) ?(m|min|mins|minutes?|h|hr|hrs|hours?|d|days?|w|weeks?)$`)
	reClock = regexp.MustCompile(`^(\d{1,2})(?:[:.](\d{2}))?(am|pm)?$`)
	reISO   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reDM    = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})(?:/(\d{2,4}))?$`)
)

func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func weekday(w string) int {
	for i, d := range []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} {
		if strings.HasPrefix(w, d) && len(w) <= 9 {
			return i
		}
	}
	return -1
}

func month(w string) int {
	if len(w) < 3 {
		return 0
	}
	for i, mo := range []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"} {
		if strings.HasPrefix(w, mo) {
			return i + 1
		}
	}
	return 0
}

// SendTime says when, the way people say it: "tomorrow 09:00", "Fri 18:00",
// "in 25 min".
func SendTime(t, now time.Time) string {
	d := t.Sub(now)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("in %d min", int(d.Minutes()+0.5))
	case midnight(t).Equal(midnight(now)):
		return "today " + t.Format("15:04")
	case midnight(t).Equal(midnight(now).AddDate(0, 0, 1)):
		return "tomorrow " + t.Format("15:04")
	case d < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	case t.Year() == now.Year():
		return t.Format("02 Jan 15:04")
	}
	return t.Format("02 Jan 2006 15:04")
}
