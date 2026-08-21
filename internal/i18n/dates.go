package i18n

import (
	"fmt"
	"strings"
	"time"
)

// Swedish writes weekdays and months in lower case and dates as "18 augusti";
// English capitalises them and says "18 August". Keeping both here means a
// page never mixes the two conventions.
var weekdays = map[Lang][7]string{
	SV: {"söndag", "måndag", "tisdag", "onsdag", "torsdag", "fredag", "lördag"},
	EN: {"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"},
}

var weekdaysShort = map[Lang][7]string{
	SV: {"sön", "mån", "tis", "ons", "tor", "fre", "lör"},
	EN: {"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
}

// weekdaysPlural is "tisdagar" / "Tuesdays" — the recurring evening rather
// than one particular day.
var weekdaysPlural = map[Lang][7]string{
	SV: {"söndagar", "måndagar", "tisdagar", "onsdagar", "torsdagar", "fredagar", "lördagar"},
	EN: {"Sundays", "Mondays", "Tuesdays", "Wednesdays", "Thursdays", "Fridays", "Saturdays"},
}

var months = map[Lang][12]string{
	SV: {"januari", "februari", "mars", "april", "maj", "juni",
		"juli", "augusti", "september", "oktober", "november", "december"},
	EN: {"January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December"},
}

var monthsShort = map[Lang][12]string{
	SV: {"jan", "feb", "mar", "apr", "maj", "jun", "jul", "aug", "sep", "okt", "nov", "dec"},
	EN: {"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"},
}

func lang(l Lang) Lang {
	if l == EN {
		return EN
	}
	return SV
}

// Weekday returns "tisdag" or "Tuesday".
func Weekday(l Lang, t time.Time) string { return weekdays[lang(l)][int(t.Weekday())] }

// WeekdayName is the same for a bare weekday value.
func WeekdayName(l Lang, wd time.Weekday) string { return weekdays[lang(l)][int(wd)] }

// WeekdayPlural returns "tisdagar" or "Tuesdays".
func WeekdayPlural(l Lang, wd time.Weekday) string { return weekdaysPlural[lang(l)][int(wd)] }

// WeekdayShort returns "tis" or "Tue".
func WeekdayShort(l Lang, t time.Time) string { return weekdaysShort[lang(l)][int(t.Weekday())] }

// Month returns "augusti" or "August".
func Month(l Lang, t time.Time) string { return months[lang(l)][int(t.Month())-1] }

// MonthShort returns "aug" or "Aug".
func MonthShort(l Lang, t time.Time) string { return monthsShort[lang(l)][int(t.Month())-1] }

// DateLong renders "tisdag 25 augusti" or "Tuesday 25 August".
func DateLong(l Lang, t time.Time) string {
	return fmt.Sprintf("%s %d %s", Weekday(l, t), t.Day(), Month(l, t))
}

// DateLongYear adds the year.
func DateLongYear(l Lang, t time.Time) string {
	return fmt.Sprintf("%s %d %s %d", Weekday(l, t), t.Day(), Month(l, t), t.Year())
}

// DateShort renders "25 aug" or "25 Aug".
func DateShort(l Lang, t time.Time) string {
	return fmt.Sprintf("%d %s", t.Day(), MonthShort(l, t))
}

// DateTime renders a moment, as a deadline is written.
func DateTime(l Lang, t time.Time) string {
	return fmt.Sprintf("%s %s", DateLong(l, t), Clock(t))
}

// Clock renders "18:00". Both languages use the 24-hour clock here: the house
// is in Sweden, and a dinner at 6 is not a thing anybody would write.
func Clock(t time.Time) string { return t.Format("15:04") }

// TitleCase upper-cases the first rune, for a Swedish weekday starting a
// sentence. English weekdays are already capitalised, so this is a no-op there.
func TitleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// JoinWeekdays renders the dinner evenings as "tisdagar och torsdagar" or
// "Tuesdays and Thursdays".
func JoinWeekdays(l Lang, wd []time.Weekday) string {
	names := make([]string, 0, len(wd))
	for _, d := range wd {
		names = append(names, WeekdayPlural(l, d))
	}
	switch len(names) {
	case 0:
		return T(l, "weekdays.none")
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + T(l, "and") + " " + names[len(names)-1]
}

// Count renders "3 vuxna" or "3 adults". The unit names a row in the
// catalogue, which holds the singular and plural of each language.
func Count(l Lang, unit string, n int) string {
	return fmt.Sprintf("%d %s", n, Plural(l, unit, n))
}

// Plural returns just the noun, for a sentence that already has the number.
func Plural(l Lang, unit string, n int) string {
	key := "unit." + unit + ".many"
	if n == 1 {
		key = "unit." + unit + ".one"
	}
	return T(l, key)
}

// Countdown says how long is left in words.
func Countdown(l Lang, until, now time.Time) string {
	d := until.Sub(now)
	switch {
	case d <= 0:
		return T(l, "countdown.closed")
	case d < time.Hour:
		return T(l, "countdown.minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		return T(l, "countdown.hours", int(d.Hours()))
	}
	return T(l, "countdown.days", int(d.Hours()/24))
}
