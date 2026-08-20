package dinner

import (
	"database/sql"
	"testing"
	"time"

	"github.com/O5ten/dinners/internal/store"
)

var loc = mustLoad("Europe/Stockholm")

func mustLoad(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func date(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

// The house's rule: everything served in a week must be registered by Friday
// night the week before, so the cooking team can shop over the weekend.
func TestDeadlineIsTheFridayBeforeTheDinnerWeek(t *testing.T) {
	d := Deadline{Weekday: time.Friday, Minutes: 23*60 + 59, WeeksBefore: 1}

	tests := []struct {
		dinner string
		want   string
	}{
		// Tuesday and Thursday of the same week share one deadline.
		{"2026-08-25", "2026-08-21 23:59"},
		{"2026-08-27", "2026-08-21 23:59"},
		{"2026-09-01", "2026-08-28 23:59"},
		// A Monday dinner belongs to its own week, not the previous one.
		{"2026-08-24", "2026-08-21 23:59"},
		// A Sunday dinner is the last day of its week.
		{"2026-08-30", "2026-08-21 23:59"},
		// Across the new year: the dinner's week starts Mon 4 Jan, so the
		// deadline is the Friday of the week before — New Year's Day.
		{"2027-01-05", "2027-01-01 23:59"},
	}
	for _, tc := range tests {
		got := d.For(date(tc.dinner), loc).Format("2006-01-02 15:04")
		if got != tc.want {
			t.Errorf("deadline for %s = %s, want %s", tc.dinner, got, tc.want)
		}
	}
}

func TestDeadlineWeeksBeforeAndWeekday(t *testing.T) {
	// Zero weeks before puts the deadline inside the dinner's own week.
	d := Deadline{Weekday: time.Monday, Minutes: 12 * 60, WeeksBefore: 0}
	got := d.For(date("2026-08-25"), loc).Format("2006-01-02 15:04")
	if want := "2026-08-24 12:00"; got != want {
		t.Errorf("same-week deadline = %s, want %s", got, want)
	}
	// Two weeks before steps back a fortnight.
	d = Deadline{Weekday: time.Friday, Minutes: 0, WeeksBefore: 2}
	got = d.For(date("2026-08-25"), loc).Format("2006-01-02 15:04")
	if want := "2026-08-14 00:00"; got != want {
		t.Errorf("two-week deadline = %s, want %s", got, want)
	}
}

// Daylight saving moves the clock inside the window between the deadline and
// the dinner; neither may drift.
func TestDeadlineSurvivesDaylightSaving(t *testing.T) {
	d := Deadline{Weekday: time.Friday, Minutes: 23*60 + 59, WeeksBefore: 1}
	// Sweden moves the clock back on the last Sunday of October (2026-10-25),
	// which falls between this deadline and its dinner.
	got := d.For(date("2026-10-27"), loc)
	if want := "2026-10-23 23:59"; got.Format("2006-01-02 15:04") != want {
		t.Errorf("deadline = %s, want %s", got.Format("2006-01-02 15:04"), want)
	}
	if got.Hour() != 23 || got.Minute() != 59 {
		t.Errorf("deadline clock drifted to %s", got.Format("15:04"))
	}
}

func params() Params {
	return Params{
		Loc:            loc,
		ServingMinutes: 18 * 60,
		Deadline:       Deadline{Weekday: time.Friday, Minutes: 23*60 + 59, WeeksBefore: 1},
	}
}

func teams(n int) []store.Team {
	out := make([]store.Team, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, store.Team{
			ID: int64(i + 1), Name: "Lag " + string(rune('1'+i)),
			Position: i, Active: true,
		})
	}
	return out
}

func season() store.Season {
	return store.Season{
		ID: 1, Name: "Höst", Start: "2026-08-25", End: "2026-09-24",
		Weekdays: []time.Weekday{time.Tuesday, time.Thursday},
	}
}

func TestBuildGeneratesOnlyTheSeasonsWeekdays(t *testing.T) {
	sched := Build([]store.Season{season()}, teams(4), nil, nil, params())
	if len(sched.Dinners) == 0 {
		t.Fatal("no dinners generated")
	}
	for _, d := range sched.Dinners {
		if wd := d.Weekday(); wd != time.Tuesday && wd != time.Thursday {
			t.Errorf("%s is a %s", d.Key, wd)
		}
		if d.Key < "2026-08-25" || d.Key > "2026-09-24" {
			t.Errorf("%s falls outside the season", d.Key)
		}
		if d.Serving.Hour() != 18 {
			t.Errorf("%s is served at %s", d.Key, d.Serving.Format("15:04"))
		}
	}
	// 25 Aug to 24 Sep inclusive: five Tuesdays and five Thursdays.
	if len(sched.Dinners) != 10 {
		t.Errorf("got %d dinners, want 10", len(sched.Dinners))
	}
}

func TestRotationTakesTurnsAndWrapsAround(t *testing.T) {
	sched := Build([]store.Season{season()}, teams(4), nil, nil, params())
	want := []string{"Lag 1", "Lag 2", "Lag 3", "Lag 4", "Lag 1",
		"Lag 2", "Lag 3", "Lag 4", "Lag 1", "Lag 2"}
	for i, d := range sched.Dinners {
		if d.Team == nil {
			t.Fatalf("%s has no team", d.Key)
		}
		if d.Team.Name != want[i] {
			t.Errorf("%s: team %s, want %s", d.Key, d.Team.Name, want[i])
		}
		if d.Assigned {
			t.Errorf("%s is marked hand-assigned but came from the rotation", d.Key)
		}
	}
}

// A new season should be able to carry on where the last one stopped.
func TestRotationOffsetShiftsTheStartingTeam(t *testing.T) {
	se := season()
	se.RotationOffset = 2
	sched := Build([]store.Season{se}, teams(4), nil, nil, params())
	if got := sched.Dinners[0].Team.Name; got != "Lag 3" {
		t.Errorf("first team = %s, want Lag 3", got)
	}
}

func TestInactiveTeamsAreSkipped(t *testing.T) {
	all := teams(4)
	all[1].Active = false
	sched := Build([]store.Season{season()}, all, nil, nil, params())
	for _, d := range sched.Dinners {
		if d.Team.Name == "Lag 2" {
			t.Fatalf("%s was given the inactive Lag 2", d.Key)
		}
	}
	want := []string{"Lag 1", "Lag 3", "Lag 4", "Lag 1"}
	for i, w := range want {
		if got := sched.Dinners[i].Team.Name; got != w {
			t.Errorf("dinner %d: team %s, want %s", i, got, w)
		}
	}
}

func TestNoTeamsLeavesTheScheduleUnassigned(t *testing.T) {
	sched := Build([]store.Season{season()}, nil, nil, nil, params())
	if len(sched.Dinners) != 10 {
		t.Fatalf("got %d dinners, want 10", len(sched.Dinners))
	}
	for _, d := range sched.Dinners {
		if d.Team != nil {
			t.Errorf("%s got a team out of nowhere", d.Key)
		}
	}
}

func TestOverridesWinOverTheRotation(t *testing.T) {
	overrides := map[string]store.Override{
		"2026-08-27": {Date: "2026-08-27", TeamID: sql.NullInt64{Int64: 4, Valid: true}},
		"2026-09-01": {Date: "2026-09-01", Cancelled: true, Note: "Festkommittén har lokalen"},
	}
	sched := Build([]store.Season{season()}, teams(4), overrides, nil, params())

	swapped, ok := sched.Find("2026-08-27")
	if !ok {
		t.Fatal("27 Aug missing")
	}
	if swapped.Team.Name != "Lag 4" {
		t.Errorf("swapped team = %s, want Lag 4", swapped.Team.Name)
	}
	if !swapped.Assigned {
		t.Error("a hand-picked team should be marked Assigned")
	}

	off, _ := sched.Find("2026-09-01")
	if !off.Cancelled || off.Note == "" {
		t.Errorf("cancellation not applied: %+v", off)
	}
	// A cancelled evening has nobody cooking it.
	if off.Team != nil {
		t.Errorf("a cancelled dinner should have no team, got %s", off.Team.Name)
	}
	// And it does not use up a turn: the team that would have cooked it takes
	// the next one instead. 25 Aug went to Lag 1, 27 Aug was handed to Lag 4
	// by hand (still consuming turn two), 1 Sep is off — so Lag 3 cooks 3 Sep.
	after, _ := sched.Find("2026-09-03")
	if after.Team.Name != "Lag 3" {
		t.Errorf("team after a cancellation = %s, want Lag 3", after.Team.Name)
	}
}

// An override naming a team that has since been deleted must not resurrect it.
func TestOverrideWithUnknownTeamFallsBackToRotation(t *testing.T) {
	overrides := map[string]store.Override{
		"2026-08-27": {Date: "2026-08-27", TeamID: sql.NullInt64{Int64: 99, Valid: true}},
	}
	sched := Build([]store.Season{season()}, teams(4), overrides, nil, params())
	d, _ := sched.Find("2026-08-27")
	if d.Team == nil || d.Team.Name != "Lag 2" {
		t.Errorf("team = %v, want the rotation's Lag 2", d.Team)
	}
	if d.Assigned {
		t.Error("a dangling override should not count as hand-assigned")
	}
}

func TestOverlappingSeasonsDoNotDoubleUpAnEvening(t *testing.T) {
	a := season()
	b := season()
	b.ID, b.Name = 2, "Överlapp"
	sched := Build([]store.Season{a, b}, teams(4), nil, nil, params())
	seen := map[string]bool{}
	for _, d := range sched.Dinners {
		if seen[d.Key] {
			t.Fatalf("%s appears twice", d.Key)
		}
		seen[d.Key] = true
	}
}

func TestOpenClosedAndOver(t *testing.T) {
	sched := Build([]store.Season{season()}, teams(4), nil, nil, params())
	d, _ := sched.Find("2026-08-25")

	before := time.Date(2026, 8, 20, 12, 0, 0, 0, loc)
	atDeadline := d.Closes
	afterDeadline := d.Closes.Add(time.Minute)
	dayAfter := date("2026-08-27").Add(time.Hour)

	if !d.Open(before) {
		t.Error("should be open a week before")
	}
	if d.Open(atDeadline) {
		t.Error("should be shut exactly at the deadline")
	}
	if !d.Closed(afterDeadline) {
		t.Error("should be closed-but-coming just after the deadline")
	}
	if d.Over(afterDeadline) {
		t.Error("should not be over before it happens")
	}
	if !d.Over(dayAfter) {
		t.Error("should be over the day after")
	}
	// A cancelled evening is never open.
	d.Cancelled = true
	if d.Open(before) || d.Closed(afterDeadline) {
		t.Error("a cancelled dinner is neither open nor closed-and-coming")
	}
}

func TestUpcomingSkipsWhatHasBeenServed(t *testing.T) {
	sched := Build([]store.Season{season()}, teams(4), nil, nil, params())
	now := date("2026-09-05")
	up := sched.Upcoming(now, 0)
	if len(up) == 0 {
		t.Fatal("nothing upcoming")
	}
	for _, d := range up {
		if d.Key < "2026-09-08" {
			t.Errorf("%s is in the past", d.Key)
		}
	}
	if got := sched.Upcoming(now, 2); len(got) != 2 {
		t.Errorf("limit ignored: got %d", len(got))
	}
}

func TestWeekStartIsMonday(t *testing.T) {
	for _, day := range []string{"2026-08-24", "2026-08-25", "2026-08-30"} {
		got := WeekStart(date(day), loc)
		if got.Weekday() != time.Monday || got.Format("2006-01-02") != "2026-08-24" {
			t.Errorf("WeekStart(%s) = %s", day, got.Format("2006-01-02 Mon"))
		}
	}
}

// A school holiday or a public holiday takes evenings out of the calendar.
func TestBreaksRemoveEveningsEntirely(t *testing.T) {
	breaks := []store.Break{{ID: 1, Name: "Höstlov", Start: "2026-08-31", End: "2026-09-06"}}
	sched := Build([]store.Season{season()}, teams(4), nil, breaks, params())

	for _, gone := range []string{"2026-09-01", "2026-09-03"} {
		if _, ok := sched.Find(gone); ok {
			t.Errorf("%s falls in the break and should not exist", gone)
		}
	}
	if _, ok := sched.Find("2026-08-27"); !ok {
		t.Error("27 Aug is outside the break and should still be there")
	}
	if _, ok := sched.Find("2026-09-08"); !ok {
		t.Error("8 Sep is after the break and should still be there")
	}
	if len(sched.Dinners) != 8 {
		t.Errorf("got %d dinners, want 8 (10 minus the two in the break)", len(sched.Dinners))
	}
}

// The point of a break: the teams pick up where they left off, so nobody loses
// a turn and nobody cooks twice in a row.
func TestBreaksPauseTheRotationRatherThanSkippingTeams(t *testing.T) {
	breaks := []store.Break{{ID: 1, Name: "Höstlov", Start: "2026-08-31", End: "2026-09-06"}}
	sched := Build([]store.Season{season()}, teams(4), nil, breaks, params())

	want := map[string]string{
		"2026-08-25": "Lag 1",
		"2026-08-27": "Lag 2",
		// 1 and 3 Sep are the break.
		"2026-09-08": "Lag 3",
		"2026-09-10": "Lag 4",
		"2026-09-15": "Lag 1",
	}
	for key, team := range want {
		d, ok := sched.Find(key)
		if !ok {
			t.Fatalf("%s missing", key)
		}
		if d.Team.Name != team {
			t.Errorf("%s: team %s, want %s", key, d.Team.Name, team)
		}
	}
}

func TestBreakCoversItsWholeRangeInclusive(t *testing.T) {
	b := store.Break{Start: "2026-10-26", End: "2026-11-01"}
	for _, in := range []string{"2026-10-26", "2026-10-29", "2026-11-01"} {
		if !b.Covers(in) {
			t.Errorf("%s should be covered", in)
		}
	}
	for _, out := range []string{"2026-10-25", "2026-11-02"} {
		if b.Covers(out) {
			t.Errorf("%s should not be covered", out)
		}
	}
}

func TestBreaksBetweenFindsThePauseInAList(t *testing.T) {
	breaks := []store.Break{
		{ID: 1, Name: "Höstlov", Start: "2026-08-31", End: "2026-09-06"},
		{ID: 2, Name: "Jullov", Start: "2026-12-21", End: "2027-01-06"},
	}
	sched := Build([]store.Season{season()}, teams(4), nil, breaks, params())

	got := sched.BreaksBetween("2026-08-27", "2026-09-08")
	if len(got) != 1 || got[0].Name != "Höstlov" {
		t.Errorf("BreaksBetween = %+v, want just Höstlov", got)
	}
	if got := sched.BreaksBetween("2026-08-25", "2026-08-27"); len(got) != 0 {
		t.Errorf("no break sits between 25 and 27 Aug, got %+v", got)
	}
}

// Several breaks, including one that swallows a whole month.
func TestOverlappingAndAdjacentBreaks(t *testing.T) {
	breaks := []store.Break{
		{ID: 1, Name: "A", Start: "2026-08-25", End: "2026-09-10"},
		{ID: 2, Name: "B", Start: "2026-09-05", End: "2026-09-17"},
	}
	sched := Build([]store.Season{season()}, teams(4), nil, breaks, params())
	// Only 22 and 24 Sep survive.
	if len(sched.Dinners) != 2 {
		t.Fatalf("got %d dinners, want 2: %v", len(sched.Dinners), keys(sched))
	}
	if sched.Dinners[0].Team.Name != "Lag 1" {
		t.Errorf("first dinner after the breaks = %s, want Lag 1", sched.Dinners[0].Team.Name)
	}
}

func keys(s Schedule) []string {
	var out []string
	for _, d := range s.Dinners {
		out = append(out, d.Key)
	}
	return out
}
