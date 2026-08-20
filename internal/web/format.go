package web

import (
	"fmt"
	"strings"
	"time"
)

var svWeekdays = [...]string{"söndag", "måndag", "tisdag", "onsdag", "torsdag", "fredag", "lördag"}
var svWeekdaysShort = [...]string{"sön", "mån", "tis", "ons", "tor", "fre", "lör"}
var svMonths = [...]string{"januari", "februari", "mars", "april", "maj", "juni",
	"juli", "augusti", "september", "oktober", "november", "december"}
var svMonthsShort = [...]string{"jan", "feb", "mar", "apr", "maj", "jun",
	"jul", "aug", "sep", "okt", "nov", "dec"}

// Weekday returns "måndag".
func Weekday(t time.Time) string { return svWeekdays[int(t.Weekday())] }

// WeekdayName returns "måndag" for a bare weekday value.
func WeekdayName(wd time.Weekday) string { return svWeekdays[int(wd)] }

// WeekdayShort returns "mån".
func WeekdayShort(t time.Time) string { return svWeekdaysShort[int(t.Weekday())] }

// Month returns "augusti".
func Month(t time.Time) string { return svMonths[int(t.Month())-1] }

// MonthShort returns "aug".
func MonthShort(t time.Time) string { return svMonthsShort[int(t.Month())-1] }

// DateLong renders "måndag 18 augusti".
func DateLong(t time.Time) string {
	return fmt.Sprintf("%s %d %s", Weekday(t), t.Day(), Month(t))
}

// DateLongYear renders "måndag 18 augusti 2026".
func DateLongYear(t time.Time) string {
	return fmt.Sprintf("%s %d %s %d", Weekday(t), t.Day(), Month(t), t.Year())
}

// DateShort renders "18 aug".
func DateShort(t time.Time) string {
	return fmt.Sprintf("%d %s", t.Day(), MonthShort(t))
}

// DateTime renders "fredag 21 augusti 23:59", for a deadline.
func DateTime(t time.Time) string {
	return fmt.Sprintf("%s %s", DateLong(t), Clock(t))
}

// Clock renders "18:00".
func Clock(t time.Time) string { return t.Format("15:04") }

// Countdown renders how long is left as something a human would say, for the
// line telling people when registration closes.
func Countdown(until, now time.Time) string {
	d := until.Sub(now)
	switch {
	case d <= 0:
		return "stängd"
	case d < time.Hour:
		return fmt.Sprintf("%d min kvar", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h kvar", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 dygn kvar"
	}
	return fmt.Sprintf("%d dygn kvar", days)
}

// Plural picks the right Swedish word for a count: Plural(1, "vuxen", "vuxna").
func Plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Count renders "1 vuxen" or "3 vuxna".
func Count(n int, one, many string) string {
	return fmt.Sprintf("%d %s", n, Plural(n, one, many))
}

// People renders "12 personer".
func People(n int) string { return Count(n, "person", "personer") }

// TitleCase upper-cases the first rune, for sentence starts.
func TitleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// JoinWeekdays renders a set of dinner evenings as "tisdagar och torsdagar".
func JoinWeekdays(wd []time.Weekday) string {
	names := make([]string, 0, len(wd))
	for _, d := range wd {
		names = append(names, svWeekdays[int(d)]+"ar")
	}
	switch len(names) {
	case 0:
		return "inga kvällar"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " och " + names[len(names)-1]
}
