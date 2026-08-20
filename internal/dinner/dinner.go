// Package dinner turns the stored seasons, teams and exceptions into the
// actual evenings the house eats together, and works out who cooks and when
// registration closes for each of them.
//
// Nothing here touches the database. The schedule is derived, not stored: a
// season plus a rotation is enough to say what happens every Tuesday and
// Thursday until Christmas, and only the deviations — a cancelled evening, a
// swapped team — need a row of their own.
package dinner

import (
	"sort"
	"time"

	"github.com/O5ten/dinners/internal/store"
)

// Deadline places the weekly moment when registration closes.
//
// It is weekly rather than per dinner because the cooking team shops once for
// the whole week. WeeksBefore counts back whole weeks from the Monday of the
// dinner's week; Weekday and Minutes then pick the moment inside that week.
type Deadline struct {
	Weekday     time.Weekday
	Minutes     int
	WeeksBefore int
}

// For returns the instant registration closes for a dinner on the given day.
func (d Deadline) For(date time.Time, loc *time.Location) time.Time {
	week := WeekStart(date, loc).AddDate(0, 0, -7*d.WeeksBefore)
	day := week.AddDate(0, 0, mondayIndex(d.Weekday))
	return time.Date(day.Year(), day.Month(), day.Day(),
		d.Minutes/60, d.Minutes%60, 0, 0, loc)
}

// Params are the settings a schedule is generated under.
type Params struct {
	Loc *time.Location
	// ServingMinutes is when the food is on the table, as minutes since
	// midnight.
	ServingMinutes int
	Deadline       Deadline
}

// Dinner is one evening the house eats together.
type Dinner struct {
	// Date is midnight at the start of the evening's day, and Key is the same
	// day as "2006-01-02" — the identifier used in URLs and in the database.
	Date time.Time
	Key  string
	// Serving is when the food is on the table.
	Serving time.Time
	// Closes is when registration shuts and the list is mailed out.
	Closes time.Time
	// Season is the season this evening belongs to.
	Season store.Season
	// Team is who cooks, or nil when no team is set up to take it.
	Team *store.Team
	// Assigned is true when an administrator picked the team by hand rather
	// than the rotation landing on it.
	Assigned  bool
	Cancelled bool
	Note      string
}

// Open reports whether registrations are still being accepted.
func (d Dinner) Open(now time.Time) bool {
	return !d.Cancelled && now.Before(d.Closes)
}

// Closed reports whether the deadline has passed but the dinner is still to
// come — the window where the cooking team is shopping.
func (d Dinner) Closed(now time.Time) bool {
	return !d.Cancelled && !now.Before(d.Closes) && !d.Over(now)
}

// Over reports whether the evening has already been served.
func (d Dinner) Over(now time.Time) bool {
	// A dinner counts as over at the end of its day rather than at the serving
	// time, so it stays on the start page while people are still eating.
	return now.After(d.Date.AddDate(0, 0, 1))
}

// Weekday is the evening's day of the week.
func (d Dinner) Weekday() time.Weekday { return d.Date.Weekday() }

// Week is the ISO week number, which is how the house talks about the season.
func (d Dinner) Week() int {
	_, w := d.Date.ISOWeek()
	return w
}

// Schedule holds the generated dinners of one or more seasons, and the breaks
// that were taken out of them.
type Schedule struct {
	Dinners []Dinner
	Breaks  []store.Break
	byKey   map[string]int
}

// BreaksBetween returns the breaks that start after one date and on or before
// another, so a list of evenings can show the pause that falls between two of
// them. Both bounds are "2006-01-02"; an empty after means "from the start".
func (s Schedule) BreaksBetween(after, until string) []store.Break {
	var out []store.Break
	for _, b := range s.Breaks {
		if b.Start > after && b.Start <= until {
			out = append(out, b)
		}
	}
	return out
}

// Find returns the dinner on a given date.
func (s Schedule) Find(key string) (Dinner, bool) {
	i, ok := s.byKey[key]
	if !ok {
		return Dinner{}, false
	}
	return s.Dinners[i], true
}

// Upcoming returns the dinners that have not been served yet, at most limit of
// them. A limit of zero means all of them.
func (s Schedule) Upcoming(now time.Time, limit int) []Dinner {
	var out []Dinner
	for _, d := range s.Dinners {
		if d.Over(now) {
			continue
		}
		out = append(out, d)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// Build generates every dinner in every season and hands each one to a cooking
// team.
//
// Only evenings that actually happen take a turn. An evening inside a break is
// never generated at all, and a cancelled one is generated but skipped in the
// rotation — so a school holiday or a late cancellation pauses the teams
// rather than costing one of them its turn.
func Build(seasons []store.Season, teams []store.Team, overrides map[string]store.Override,
	breaks []store.Break, p Params) Schedule {
	byID := map[int64]store.Team{}
	var rotation []store.Team
	for _, t := range teams {
		byID[t.ID] = t
		if t.Active {
			rotation = append(rotation, t)
		}
	}

	sorted := append([]store.Season(nil), seasons...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

	sched := Schedule{Breaks: sortedBreaks(breaks), byKey: map[string]int{}}
	for _, se := range sorted {
		for _, d := range seasonDinners(se, rotation, byID, overrides, breaks, p) {
			// Two seasons that overlap would otherwise produce the same
			// evening twice; the earlier one keeps it.
			if _, clash := sched.byKey[d.Key]; clash {
				continue
			}
			sched.byKey[d.Key] = len(sched.Dinners)
			sched.Dinners = append(sched.Dinners, d)
		}
	}
	return sched
}

func sortedBreaks(breaks []store.Break) []store.Break {
	out := append([]store.Break(nil), breaks...)
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// inBreak reports whether a date falls inside any break.
func inBreak(breaks []store.Break, date string) bool {
	for _, b := range breaks {
		if b.Covers(date) {
			return true
		}
	}
	return false
}

// seasonDinners walks a season day by day and picks out its dinner evenings.
func seasonDinners(se store.Season, rotation []store.Team, byID map[int64]store.Team,
	overrides map[string]store.Override, breaks []store.Break, p Params) []Dinner {
	start, err := time.ParseInLocation("2006-01-02", se.Start, p.Loc)
	if err != nil {
		return nil
	}
	end, err := time.ParseInLocation("2006-01-02", se.End, p.Loc)
	if err != nil {
		return nil
	}
	want := map[time.Weekday]bool{}
	for _, wd := range se.Weekdays {
		want[wd] = true
	}
	if len(want) == 0 {
		return nil
	}

	var out []Dinner
	// turn counts only the evenings that are really cooked, so the rotation
	// picks up where it left off after a break.
	turn := 0
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		if !want[day.Weekday()] {
			continue
		}
		key := day.Format("2006-01-02")
		if inBreak(breaks, key) {
			continue
		}
		d := Dinner{
			Date:   day,
			Key:    key,
			Season: se,
			Serving: time.Date(day.Year(), day.Month(), day.Day(),
				p.ServingMinutes/60, p.ServingMinutes%60, 0, 0, p.Loc),
			Closes: p.Deadline.For(day, p.Loc),
		}
		o, hasOverride := overrides[key]
		if hasOverride {
			d.Cancelled = o.Cancelled
			d.Note = o.Note
		}
		if d.Cancelled {
			// Nobody cooks a cancelled evening, and it does not use up a turn.
			out = append(out, d)
			continue
		}
		switch {
		case hasOverride && o.TeamID.Valid && teamExists(byID, o.TeamID.Int64):
			team := byID[o.TeamID.Int64]
			d.Team = &team
			d.Assigned = true
		case len(rotation) > 0:
			n := len(rotation)
			team := rotation[((turn+se.RotationOffset)%n+n)%n]
			d.Team = &team
		}
		// A hand-picked team still consumes the turn, so swapping one evening
		// leaves the rest of the season exactly where it was.
		turn++
		out = append(out, d)
	}
	return out
}

func teamExists(byID map[int64]store.Team, id int64) bool {
	_, ok := byID[id]
	return ok
}

// WeekStart returns midnight on the Monday of the week that date falls in.
func WeekStart(date time.Time, loc *time.Location) time.Time {
	d := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	return d.AddDate(0, 0, -mondayIndex(d.Weekday()))
}

// mondayIndex numbers the days from Monday, the way a Swedish week runs.
func mondayIndex(wd time.Weekday) int { return (int(wd) + 6) % 7 }
