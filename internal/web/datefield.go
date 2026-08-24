package web

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/i18n"
)

// Picking a date.
//
// The admin forms used <input type="date">, which sounds like the right answer
// and is not: a browser renders it in whatever order its own locale prefers,
// so a house that writes 2026-08-25 was being shown 08/25/2026 and asked to
// type into it in that order. The format is not ours to choose, and neither is
// what clicking a part of it does.
//
// So a date is three selectors instead — year, month, day, in that order,
// zero-padded, reading as the ISO 8601 date it is in every browser and every
// locale. Clicking any part of it opens the list of what that part can be,
// which is what a selector is for, and needs no JavaScript to do.
//
// The numbers are unambiguous but not friendly on their own, so the field says
// the date back in words underneath. app.js keeps that in step as the
// selectors change, and trims the days to the month — February has no 31st.

// isoMonths and isoDays are the values the month and day selectors offer. The
// day list is the longest a month can be; app.js shortens it to fit the month
// that is actually chosen, and a date that slips through anyway is refused by
// ParseDate rather than rounded into a different one.
var (
	isoMonths = padRange(1, 12)
	isoDays   = padRange(1, 31)
)

func padRange(from, to int) []string {
	out := make([]string, 0, to-from+1)
	for n := from; n <= to; n++ {
		out = append(out, fmt.Sprintf("%02d", n))
	}
	return out
}

// dateField is one date as its three selectors show it.
type dateField struct {
	// Name is the form field's name. The three selectors post Name_year,
	// Name_month and Name_day, which formDate reads back as one date.
	Name string
	// Year, Month and Day are the parts currently chosen, zero-padded, or
	// empty when the date is not set.
	Year, Month, Day string
	Required         bool
	// Years is the list the year selector offers, Months and Days the other
	// two. All three are shared slices, not copies.
	Years  []int
	Months []string
	Days   []string
	// Prose is the date in words — "25 augusti 2026" — so that a reader can
	// see that 08 is the month they meant. Empty when no date is set.
	Prose string
	// MonthNames is the twelve months in the reader's language, for app.js to
	// build Prose from as the selectors change. The page is rendered in one
	// language and the script is not, so the words have to travel with it.
	MonthNames string
}

// newDateField prepares one date for the "datefield" template. iso is the
// date as stored, YYYY-MM-DD, or empty for a date not yet chosen.
func newDateField(lang i18n.Lang, name, iso string, required bool, years []int) dateField {
	f := dateField{
		Name: name, Required: required,
		Years: years, Months: isoMonths, Days: isoDays,
		MonthNames: strings.Join(i18n.MonthNames(lang), ","),
	}
	// Only a whole, real date fills the selectors in. Half a date in the
	// database is not something to guess at.
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(iso)); err == nil {
		f.Year = fmt.Sprintf("%04d", t.Year())
		f.Month = fmt.Sprintf("%02d", int(t.Month()))
		f.Day = fmt.Sprintf("%02d", t.Day())
		f.Prose = proseDate(lang, t)
	}
	return f
}

// proseDate is the date in words, without the weekday: the field is about
// which date it is, not which day of the week, and app.js has to be able to
// say the same thing from the month names alone.
func proseDate(lang i18n.Lang, t time.Time) string {
	return fmt.Sprintf("%d %s %d", t.Day(), i18n.Month(lang, t), t.Year())
}

// formDate reads a date posted as three parts and returns it as YYYY-MM-DD,
// ready for config.ParseDate.
//
// A plain field of the same name wins when one is posted. Nothing on the site
// sends one any more, but it is the obvious thing for a script or an old
// bookmark to send, and it costs nothing to keep understanding it.
//
// Nothing chosen at all is an empty string rather than an error: the end of a
// break is optional and means "just that day". A date with only some of its
// parts is passed on as it stands, so that ParseDate is the one place that
// decides what is and is not a date.
func formDate(r *http.Request, name string) string {
	if whole := strings.TrimSpace(r.FormValue(name)); whole != "" {
		return whole
	}
	year := strings.TrimSpace(r.FormValue(name + "_year"))
	month := strings.TrimSpace(r.FormValue(name + "_month"))
	day := strings.TrimSpace(r.FormValue(name + "_day"))
	if year == "" && month == "" && day == "" {
		return ""
	}
	return pad(year, 4) + "-" + pad(month, 2) + "-" + pad(day, 2)
}

// pad widens a number the selectors posted, so that a hand-written "8" means
// August rather than nothing. Anything that is not a number is left alone for
// ParseDate to complain about by name.
func pad(raw string, width int) string {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return raw
	}
	return fmt.Sprintf("%0*d", width, n)
}

// yearOptions is the years the year selectors offer: one behind and a few
// ahead, which is the range a house plans its dinners in.
//
// Every year already in use is added to that. A season starting in 2019 must
// still be editable without the selector quietly moving it into range — a
// field that cannot show its own value is a field that changes it.
func yearOptions(world *world, now time.Time) []int {
	seen := map[int]bool{}
	for y := now.Year() - 1; y <= now.Year()+5; y++ {
		seen[y] = true
	}
	for _, se := range world.Seasons {
		seen[isoYear(se.Start)] = true
		seen[isoYear(se.End)] = true
	}
	for _, b := range world.Breaks {
		seen[isoYear(b.Start)] = true
		seen[isoYear(b.End)] = true
	}
	out := make([]int, 0, len(seen))
	for y := range seen {
		// Zero is what an unset or unparseable date yields; it is not a year.
		if y > 0 {
			out = append(out, y)
		}
	}
	sort.Ints(out)
	return out
}

// isoYear reads the year off a stored YYYY-MM-DD date.
func isoYear(iso string) int {
	if len(iso) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(iso[:4])
	return n
}
