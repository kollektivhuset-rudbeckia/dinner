package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

var now = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func reg(date, username, name string, adults, children int) Registration {
	return Registration{
		ID: "id-" + username + "-" + date, Date: date, Kind: KindMember,
		Member: username, MMUserID: "u-" + username,
		Name: name, Adults: adults, Children: children,
		Diet:  DietOmnivore,
		Token: "tok-" + username + "-" + date, CreatedAt: now, UpdatedAt: now,
	}
}

func TestEmptyDatabase(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	empty, err := st.Empty(ctx)
	if err != nil || !empty {
		t.Fatalf("Empty = %v, %v; want true, nil", empty, err)
	}
	if _, err := st.SaveTeam(ctx, Team{Name: "Lag 1", Active: true}); err != nil {
		t.Fatal(err)
	}
	if empty, _ := st.Empty(ctx); empty {
		t.Error("a database with a team in it is not empty")
	}
}

func TestSettingsFallBackToTheDefaults(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	def := Settings{DeadlineWeekday: time.Friday, DeadlineMinutes: 1439,
		DeadlineWeeksBefore: 1, GuestOpen: true}

	got, err := st.Settings(ctx, def)
	if err != nil {
		t.Fatal(err)
	}
	if got != def {
		t.Errorf("unstored settings = %+v, want the defaults %+v", got, def)
	}
	if stored, _ := st.SettingsStored(ctx); stored {
		t.Error("nothing has been stored yet")
	}

	want := Settings{DeadlineWeekday: time.Wednesday, DeadlineMinutes: 600,
		DeadlineWeeksBefore: 2, GuestOpen: false}
	if err := st.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Settings(ctx, def)
	if got != want {
		t.Errorf("stored settings = %+v, want %+v", got, want)
	}
	if stored, _ := st.SettingsStored(ctx); !stored {
		t.Error("settings should now be stored")
	}
}

// The same household answering twice must leave one row, not two.
func TestMemberRegistrationIsReplacedNotDuplicated(t *testing.T) {
	st := open(t)
	ctx := context.Background()

	first := reg("2026-08-25", "anna", "Anna", 2, 1)
	if err := st.SaveRegistration(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := reg("2026-08-25", "anna", "Anna Andersson", 1, 0)
	second.ID = "a-completely-different-id"
	second.UpdatedAt = now.Add(time.Hour)
	if err := st.SaveRegistration(ctx, second); err != nil {
		t.Fatal(err)
	}

	all, err := st.Registrations(ctx, "2026-08-25")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d registrations, want 1: %+v", len(all), all)
	}
	got := all[0]
	if got.Adults != 1 || got.Children != 0 || got.Name != "Anna Andersson" {
		t.Errorf("the second answer did not win: %+v", got)
	}
	// The row keeps its identity, so a link to it does not break.
	if got.ID != first.ID {
		t.Errorf("ID = %q, want the original %q", got.ID, first.ID)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want the original %v", got.CreatedAt, now)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Error("UpdatedAt should have moved")
	}
}

// Two visitors are always two rows: they have no account here to be one by.
func TestGuestRegistrationsAreAlwaysSeparateRows(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	for i, name := range []string{"Kalle", "Maja"} {
		r := Registration{
			ID: "g" + string(rune('0'+i)), Date: "2026-08-25", Kind: KindGuest,
			Name: name, Adults: 1, Diet: DietOmnivore,
			Token: "t" + string(rune('0'+i)), CreatedAt: now, UpdatedAt: now,
		}
		if err := st.SaveRegistration(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := st.Registrations(ctx, "2026-08-25")
	if len(all) != 2 {
		t.Fatalf("got %d guest rows, want 2", len(all))
	}
}

func TestRegistrationByToken(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	r := Registration{ID: "g1", Date: "2026-08-25", Kind: KindGuest, Name: "Kalle",
		Adults: 2, Diet: DietVegan, Token: "secret-token", CreatedAt: now, UpdatedAt: now}
	if err := st.SaveRegistration(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := st.RegistrationByToken(ctx, "secret-token")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.Name != "Kalle" {
		t.Errorf("Name = %q", got.Name)
	}
	if _, err := st.RegistrationByToken(ctx, "wrong"); err != ErrNotFound {
		t.Errorf("unknown token: err = %v, want ErrNotFound", err)
	}
	// An empty token must never match a row, whatever is in the table.
	if _, err := st.RegistrationByToken(ctx, ""); err != ErrNotFound {
		t.Errorf("empty token: err = %v, want ErrNotFound", err)
	}
}

func TestMemberRegistrationLookupAndRange(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	for _, r := range []Registration{
		reg("2026-08-25", "anna", "Anna", 2, 0),
		reg("2026-08-27", "anna", "Anna", 1, 0),
		reg("2026-09-01", "bo", "Bo", 1, 0),
	} {
		if err := st.SaveRegistration(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.MemberRegistration(ctx, "2026-08-27", "anna")
	if err != nil || got.Adults != 1 {
		t.Errorf("MemberRegistration = %+v, %v", got, err)
	}
	if _, err := st.MemberRegistration(ctx, "2026-08-27", "bo"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}

	between, err := st.RegistrationsBetween(ctx, "2026-08-25", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(between) != 2 {
		t.Errorf("got %d in range, want 2", len(between))
	}

	mine, err := st.MemberRegistrations(ctx, "anna", "2026-08-26")
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].Date != "2026-08-27" {
		t.Errorf("MemberRegistrations = %+v", mine)
	}
}

func TestDeleteRegistration(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	r := reg("2026-08-25", "anna", "Anna", 1, 0)
	if err := st.SaveRegistration(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRegistration(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRegistration(ctx, r.ID); err != ErrNotFound {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

func TestStandingUpsertAndClear(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	base := Standing{ID: "s1", Member: "anna.andersson", Weekday: time.Tuesday,
		Name: "Anna", Adults: 2, Children: 1, Diet: DietVegetarian, UpdatedAt: now}
	if err := st.SaveStanding(ctx, base); err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.ID, changed.Adults = "s2", 3
	if err := st.SaveStanding(ctx, changed); err != nil {
		t.Fatal(err)
	}

	list, err := st.StandingByMember(ctx, "anna.andersson")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d standing rows, want 1", len(list))
	}
	if list[0].Adults != 3 {
		t.Errorf("Adults = %d, want 3", list[0].Adults)
	}

	// A standing registration for nobody means none at all.
	empty := base
	empty.Adults, empty.Children = 0, 0
	if err := st.SaveStanding(ctx, empty); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.StandingByMember(ctx, "anna.andersson"); len(list) != 0 {
		t.Errorf("saving nobody should have cleared it, got %+v", list)
	}
}

func TestStandingIsPerWeekday(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	for _, wd := range []time.Weekday{time.Tuesday, time.Thursday} {
		if err := st.SaveStanding(ctx, Standing{
			ID: "s" + wd.String(), Member: "anna.andersson", Weekday: wd,
			Name: "Anna", Adults: 1, Diet: DietOmnivore, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := st.StandingByMember(ctx, "anna.andersson"); len(list) != 2 {
		t.Errorf("got %d rows, want one per weekday", len(list))
	}
	tue, err := st.StandingFor(ctx, time.Tuesday)
	if err != nil {
		t.Fatal(err)
	}
	if len(tue) != 1 {
		t.Errorf("StandingFor(Tuesday) = %d rows, want 1", len(tue))
	}
	if err := st.DeleteStanding(ctx, "anna.andersson", time.Tuesday); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.StandingByMember(ctx, "anna.andersson"); len(list) != 1 {
		t.Errorf("after deleting Tuesday: %+v", list)
	}
}

// An override that says nothing should not linger as a row.
func TestEmptyOverrideIsRemoved(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	if err := st.SaveOverride(ctx, Override{Date: "2026-08-25", Cancelled: true, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Overrides(ctx, "2026-01-01", "2026-12-31")
	if len(got) != 1 {
		t.Fatalf("got %d overrides, want 1", len(got))
	}
	if err := st.SaveOverride(ctx, Override{Date: "2026-08-25", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Overrides(ctx, "2026-01-01", "2026-12-31")
	if len(got) != 0 {
		t.Errorf("an override with nothing in it should be deleted, got %+v", got)
	}
}

func TestOverrideKeepsTeamAndNote(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	if err := st.SaveOverride(ctx, Override{
		Date: "2026-08-25", TeamID: sql.NullInt64{Int64: 3, Valid: true},
		Note: "  Temakväll  ", UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Overrides(ctx, "2026-08-01", "2026-08-31")
	o := got["2026-08-25"]
	if !o.TeamID.Valid || o.TeamID.Int64 != 3 {
		t.Errorf("TeamID = %+v", o.TeamID)
	}
	if o.Note != "Temakväll" {
		t.Errorf("Note = %q, want it trimmed", o.Note)
	}
}

func TestDeletingATeamReleasesTheEveningsItHeld(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	id, err := st.SaveTeam(ctx, Team{Name: "Lag 1", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveOverride(ctx, Override{
		Date: "2026-08-25", TeamID: sql.NullInt64{Int64: id, Valid: true}, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTeam(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Overrides(ctx, "2026-08-01", "2026-08-31")
	if o, ok := got["2026-08-25"]; ok && o.TeamID.Valid {
		t.Errorf("the evening still points at the deleted team: %+v", o)
	}
}

// names returns the teams in rotation order, which is what every assertion
// about ordering is really about.
func names(t *testing.T, st *Store) []string {
	t.Helper()
	all, err := st.Teams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, team := range all {
		out = append(out, team.Name)
	}
	return out
}

func equal(a []string, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func addTeams(t *testing.T, st *Store, names ...string) []int64 {
	t.Helper()
	var ids []int64
	for _, name := range names {
		id, err := st.SaveTeam(context.Background(), Team{Name: name, Active: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// A new team goes last, and the caller does not get to pick a place — that is
// what keeps two teams from sharing one.
func TestNewTeamsGoLastInTheRotation(t *testing.T) {
	st := open(t)
	addTeams(t, st, "Första", "Andra", "Tredje")
	if got := names(t, st); !equal(got, "Första", "Andra", "Tredje") {
		t.Errorf("order = %v", got)
	}
	// Even when the caller tries to jump the queue.
	if _, err := st.SaveTeam(context.Background(), Team{Name: "Smitare", Position: 0, Active: true}); err != nil {
		t.Fatal(err)
	}
	if got := names(t, st); !equal(got, "Första", "Andra", "Tredje", "Smitare") {
		t.Errorf("order = %v, want the newcomer last", got)
	}
}

func TestReorderTeamsWritesAWholePermutation(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	ids := addTeams(t, st, "A", "B", "C", "D")

	if err := st.ReorderTeams(ctx, []int64{ids[3], ids[1], ids[0], ids[2]}); err != nil {
		t.Fatal(err)
	}
	if got := names(t, st); !equal(got, "D", "B", "A", "C") {
		t.Errorf("order = %v", got)
	}
	all, _ := st.Teams(ctx)
	for i, team := range all {
		if team.Position != i {
			t.Errorf("%s has position %d, want %d", team.Name, team.Position, i)
		}
	}
}

// Whatever the browser sends, the stored order has to come out valid.
func TestReorderTeamsSurvivesRubbishInput(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	ids := addTeams(t, st, "A", "B", "C")

	tests := []struct {
		name string
		send []int64
		want []string
	}{
		{"an id that does not exist", []int64{ids[2], 9999, ids[0]}, []string{"C", "A", "B"}},
		{"the same id twice", []int64{ids[1], ids[1], ids[0]}, []string{"B", "A", "C"}},
		{"a team left out", []int64{ids[2]}, []string{"C", "A", "B"}},
		{"nothing at all", nil, []string{"A", "B", "C"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Start from A, B, C every time.
			if err := st.ReorderTeams(ctx, ids); err != nil {
				t.Fatal(err)
			}
			if err := st.ReorderTeams(ctx, tc.send); err != nil {
				t.Fatal(err)
			}
			if got := names(t, st); !equal(got, tc.want...) {
				t.Errorf("order = %v, want %v", got, tc.want)
			}
			all, _ := st.Teams(ctx)
			seen := map[int]bool{}
			for _, team := range all {
				if seen[team.Position] {
					t.Fatalf("two teams share position %d", team.Position)
				}
				seen[team.Position] = true
			}
		})
	}
}

// Deleting from the middle must not leave a hole for a later insert to land in.
func TestDeletingATeamClosesTheGap(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	ids := addTeams(t, st, "A", "B", "C")
	if err := st.DeleteTeam(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	all, _ := st.Teams(ctx)
	if len(all) != 2 {
		t.Fatalf("got %d teams", len(all))
	}
	for i, team := range all {
		if team.Position != i {
			t.Errorf("%s has position %d, want %d", team.Name, team.Position, i)
		}
	}
	addTeams(t, st, "D")
	if got := names(t, st); !equal(got, "A", "C", "D") {
		t.Errorf("order = %v", got)
	}
}

func TestInactiveTeamsStayInTheListButOutOfTheRotation(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	ids := addTeams(t, st, "A", "B", "C")
	all, _ := st.Teams(ctx)
	for i := range all {
		if all[i].ID == ids[1] {
			all[i].Active = false
			if _, err := st.SaveTeam(ctx, all[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := names(t, st); !equal(got, "A", "B", "C") {
		t.Errorf("an inactive team should keep its place in the list, got %v", got)
	}
	active, _ := st.ActiveTeams(ctx)
	if len(active) != 2 || active[0].Name != "A" || active[1].Name != "C" {
		t.Errorf("active = %+v", active)
	}
}

func TestSeasonsRoundTripTheirWeekdays(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	id, err := st.SaveSeason(ctx, Season{
		Name: "Höst", Start: "2026-08-25", End: "2026-12-17",
		Weekdays: []time.Weekday{time.Tuesday, time.Thursday}, RotationOffset: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := st.Seasons(ctx)
	if len(all) != 1 {
		t.Fatalf("got %d seasons", len(all))
	}
	se := all[0]
	if len(se.Weekdays) != 2 || se.Weekdays[0] != time.Tuesday || se.Weekdays[1] != time.Thursday {
		t.Errorf("Weekdays = %v", se.Weekdays)
	}
	if se.RotationOffset != 2 {
		t.Errorf("RotationOffset = %d", se.RotationOffset)
	}
	if err := st.DeleteSeason(ctx, id); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.Seasons(ctx); len(all) != 0 {
		t.Error("season not deleted")
	}
}

func TestBreaksRoundTrip(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	id, err := st.SaveBreak(ctx, Break{Name: "Höstlov", Start: "2026-10-26", End: "2026-11-01"})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := st.Breaks(ctx)
	if len(all) != 1 || all[0].Name != "Höstlov" {
		t.Fatalf("Breaks = %+v", all)
	}
	all[0].Name = "Ändrat"
	if _, err := st.SaveBreak(ctx, all[0]); err != nil {
		t.Fatal(err)
	}
	all, _ = st.Breaks(ctx)
	if len(all) != 1 || all[0].Name != "Ändrat" {
		t.Errorf("update failed: %+v", all)
	}
	if err := st.DeleteBreak(ctx, id); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.Breaks(ctx); len(all) != 0 {
		t.Error("break not deleted")
	}
}

// The mail log is what stops two servers, or one restarted server, from
// sending the same list twice.
func TestNotificationsAreRecordedOnce(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	if done, _ := st.Notified(ctx, "2026-08-25", "deadline"); done {
		t.Fatal("nothing sent yet")
	}
	if err := st.MarkNotified(ctx, "2026-08-25", "deadline", "anna", now); err != nil {
		t.Fatal(err)
	}
	if done, _ := st.Notified(ctx, "2026-08-25", "deadline"); !done {
		t.Error("should be recorded")
	}
	if err := st.MarkNotified(ctx, "2026-08-25", "deadline", "bo", now); err == nil {
		t.Error("a second record for the same dinner must be refused")
	}

	sent, err := st.SentNotifications(ctx, "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	log, ok := sent["2026-08-25"]
	if !ok || !log.SentAt.Equal(now) || log.Recipient != "anna" {
		t.Errorf("SentNotifications = %+v", sent)
	}

	if err := st.ClearNotified(ctx, "2026-08-25", "deadline"); err != nil {
		t.Fatal(err)
	}
	if done, _ := st.Notified(ctx, "2026-08-25", "deadline"); done {
		t.Error("should be forgotten so it can be sent again")
	}
}

func TestWeekdayListFormatting(t *testing.T) {
	wd := []time.Weekday{time.Tuesday, time.Thursday}
	if got := FormatWeekdayList(wd); got != "2,4" {
		t.Errorf("FormatWeekdayList = %q", got)
	}
	got := ParseWeekdayList(" 2 , 4 ")
	if len(got) != 2 || got[0] != time.Tuesday || got[1] != time.Thursday {
		t.Errorf("ParseWeekdayList = %v", got)
	}
	// Rubbish is skipped rather than becoming Sunday.
	if got := ParseWeekdayList("2,nine,4,"); len(got) != 2 {
		t.Errorf("ParseWeekdayList with junk = %v", got)
	}
}

func TestSeasonOverlap(t *testing.T) {
	base := Season{Start: "2026-08-25", End: "2026-12-17"}
	overlapping := []Season{
		{Start: "2026-08-25", End: "2026-12-17"}, // identical
		{Start: "2026-06-01", End: "2026-08-25"}, // ends on the first day
		{Start: "2026-12-17", End: "2027-03-01"}, // starts on the last day
		{Start: "2026-10-01", End: "2026-10-31"}, // wholly inside
		{Start: "2026-01-01", End: "2027-12-31"}, // wholly around
	}
	for _, other := range overlapping {
		if !base.Overlaps(other) {
			t.Errorf("%s–%s should overlap %s–%s", other.Start, other.End, base.Start, base.End)
		}
		if !other.Overlaps(base) {
			t.Errorf("overlap should be symmetric for %s–%s", other.Start, other.End)
		}
	}
	// A season may start the day after another ends.
	for _, other := range []Season{
		{Start: "2027-01-10", End: "2027-05-01"},
		{Start: "2026-01-01", End: "2026-08-24"},
		{Start: "2026-12-18", End: "2027-05-01"},
	} {
		if base.Overlaps(other) {
			t.Errorf("%s–%s should not overlap %s–%s", other.Start, other.End, base.Start, base.End)
		}
	}
}

func TestDietRoundTripsAndDefaultsSafely(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	r := reg("2026-08-25", "anna", "Anna", 2, 0)
	r.Diet = DietFlexitarian
	if err := st.SaveRegistration(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := st.MemberRegistration(ctx, "2026-08-25", "anna")
	if err != nil {
		t.Fatal(err)
	}
	if got.Diet != DietFlexitarian {
		t.Errorf("Diet = %q", got.Diet)
	}

	// Changing the diet is an edit, not a second registration.
	r.Diet = DietVegan
	if err := st.SaveRegistration(ctx, r); err != nil {
		t.Fatal(err)
	}
	all, _ := st.Registrations(ctx, "2026-08-25")
	if len(all) != 1 || all[0].Diet != DietVegan {
		t.Errorf("registrations = %+v", all)
	}
}

func TestParseDiet(t *testing.T) {
	for _, d := range Diets {
		got, ok := ParseDiet(string(d))
		if !ok || got != d {
			t.Errorf("ParseDiet(%q) = %q, %v", d, got, ok)
		}
		if !d.Valid() {
			t.Errorf("%q should be valid", d)
		}
	}
	// Anything unrecognised has to be fed something, and the unrestricted meal
	// is the safe answer.
	for _, s := range []string{"", "vegans", "VEGAN", "makrobiotisk"} {
		got, ok := ParseDiet(s)
		if ok {
			t.Errorf("ParseDiet(%q) reported success", s)
		}
		if got != DietOmnivore {
			t.Errorf("ParseDiet(%q) = %q, want the unrestricted meal", s, got)
		}
	}
	if Diet("makrobiotisk").Valid() {
		t.Error("an unknown diet must not report itself valid")
	}
}

// A database written by the two-counts build has to come across without losing
// anyone's meal. The evenings in it are long past, so they are history the
// switch to Mattermost accounts leaves alone.
func TestMigrationFromTheOldDietCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The shape the previous build wrote, diet column and all absent.
	if _, err := old.Exec(`
		CREATE TABLE registrations (
			id TEXT PRIMARY KEY, date TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'member',
			email TEXT NOT NULL DEFAULT '', name TEXT NOT NULL,
			apartment TEXT NOT NULL DEFAULT '', host TEXT NOT NULL DEFAULT '',
			adults INTEGER NOT NULL DEFAULT 0, children INTEGER NOT NULL DEFAULT 0,
			vegans INTEGER NOT NULL DEFAULT 0, vegetarians INTEGER NOT NULL DEFAULT 0,
			note TEXT NOT NULL DEFAULT '', token TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			created_ip TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE standing (
			id TEXT PRIMARY KEY, email TEXT NOT NULL, weekday INTEGER NOT NULL,
			name TEXT NOT NULL, apartment TEXT NOT NULL DEFAULT '',
			adults INTEGER NOT NULL DEFAULT 0, children INTEGER NOT NULL DEFAULT 0,
			vegans INTEGER NOT NULL DEFAULT 0, vegetarians INTEGER NOT NULL DEFAULT 0,
			note TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
		);
		INSERT INTO registrations (id, date, email, name, adults, children, vegans, vegetarians, created_at, updated_at)
		VALUES ('r1', '2020-09-15', 'a@x.se', 'Allätarna', 2, 1, 0, 0, '2020-09-01T10:00:00Z', '2020-09-01T10:00:00Z'),
		       ('r2', '2020-09-15', 'b@x.se', 'Veganen',   1, 0, 1, 0, '2020-09-01T10:00:00Z', '2020-09-01T10:00:00Z'),
		       ('r3', '2020-09-15', 'c@x.se', 'Blandat',   2, 1, 0, 2, '2020-09-01T10:00:00Z', '2020-09-01T10:00:00Z');
		INSERT INTO standing (id, email, weekday, name, adults, vegans, vegetarians, updated_at)
		VALUES ('s1', 'greta@x.se', 2, 'Greta', 1, 1, 0, '2020-09-01T10:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("opening an old database: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	byName := map[string]Diet{}
	regs, err := st.Registrations(ctx, "2020-09-15")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range regs {
		byName[r.Name] = r.Diet
	}
	want := map[string]Diet{
		"Allätarna": DietOmnivore,
		"Veganen":   DietVegan,
		// Partly vegetarian becomes vegetarian outright: that is the meal the
		// cooking team has to produce either way.
		"Blandat": DietVegetarian,
	}
	for name, diet := range want {
		if byName[name] != diet {
			t.Errorf("%s migrated to %q, want %q", name, byName[name], diet)
		}
	}
	// The standing registration is gone rather than migrated: it was keyed by
	// an address, and a standing registration with no household behind it
	// would go on adding Greta to every Tuesday with nobody able to stop it.
	if all, _ := st.AllStanding(ctx); len(all) != 0 {
		t.Errorf("standing registrations survived the switch: %+v", all)
	}

	// Opening it again must be a no-op rather than a re-migration.
	st.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer again.Close()
	if regs, _ := again.Registrations(ctx, "2020-09-15"); len(regs) != 3 {
		t.Errorf("got %d registrations after reopening", len(regs))
	}
}

// A database from the version that identified a household by its e-mail
// address. There is no way to turn an address into a Mattermost account, so the
// upgrade keeps the evenings already served and clears what would otherwise be
// counted by nobody.
func TestMigrationFromTheHouseholdAddresses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().AddDate(0, 0, -14).Format("2006-01-02")
	future := time.Now().AddDate(0, 0, 14).Format("2006-01-02")
	if _, err := old.Exec(`
		CREATE TABLE registrations (
			id TEXT PRIMARY KEY, date TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'member',
			email TEXT NOT NULL DEFAULT '', name TEXT NOT NULL,
			apartment TEXT NOT NULL DEFAULT '', host TEXT NOT NULL DEFAULT '',
			adults INTEGER NOT NULL DEFAULT 0, children INTEGER NOT NULL DEFAULT 0,
			diet TEXT NOT NULL DEFAULT 'allatare',
			note TEXT NOT NULL DEFAULT '', token TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			created_ip TEXT NOT NULL DEFAULT ''
		);
		CREATE UNIQUE INDEX idx_reg_member ON registrations (date, email) WHERE kind = 'member';
		CREATE TABLE standing (
			id TEXT PRIMARY KEY, email TEXT NOT NULL, weekday INTEGER NOT NULL,
			name TEXT NOT NULL, apartment TEXT NOT NULL DEFAULT '',
			adults INTEGER NOT NULL DEFAULT 0, children INTEGER NOT NULL DEFAULT 0,
			diet TEXT NOT NULL DEFAULT 'allatare',
			note TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
		);
		CREATE UNIQUE INDEX idx_standing ON standing (email, weekday);
		INSERT INTO registrations (id, date, kind, email, name, adults, created_at, updated_at)
		VALUES ('served', '` + past + `',   'member', 'anna@x.se', 'Anna',  2, '2020-01-01T10:00:00Z', '2020-01-01T10:00:00Z'),
		       ('coming', '` + future + `', 'member', 'anna@x.se', 'Anna',  2, '2020-01-01T10:00:00Z', '2020-01-01T10:00:00Z'),
		       ('visitor','` + future + `', 'guest',  '',          'Kalle', 1, '2020-01-01T10:00:00Z', '2020-01-01T10:00:00Z');
		INSERT INTO standing (id, email, weekday, name, adults, updated_at)
		VALUES ('s1', 'anna@x.se', 2, 'Anna', 2, '2020-01-01T10:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("opening an old database: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// The evening already served is the house's history and stays exactly as
	// it was, list and all — only without a household to edit it by.
	served, _ := st.Registrations(ctx, past)
	if len(served) != 1 || served[0].Name != "Anna" {
		t.Errorf("the served evening should be untouched, got %+v", served)
	}
	if served[0].Member != "" {
		t.Errorf("Member = %q, want empty: there was nothing to fill it from", served[0].Member)
	}

	// The evening still to come is cleared, so that Anna registering again is
	// one household rather than two. The guest keeps their row: they were never
	// identified by an address in the first place.
	coming, _ := st.Registrations(ctx, future)
	if len(coming) != 1 || coming[0].Kind != KindGuest {
		t.Errorf("the coming evening should hold only the guest, got %+v", coming)
	}
	if all, _ := st.AllStanding(ctx); len(all) != 0 {
		t.Errorf("standing registrations survived: %+v", all)
	}

	// And the address itself is gone from both tables.
	for _, table := range []string{"registrations", "standing"} {
		var n int
		if err := st.db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = 'email'`, table).
			Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s still has an email column", table)
		}
	}

	// Anna registers again, by account this time, and is one household.
	if err := st.SaveRegistration(ctx, reg(future, "anna.andersson", "Anna", 2, 0)); err != nil {
		t.Fatal(err)
	}
	again := reg(future, "anna.andersson", "Anna", 3, 0)
	again.ID = "another"
	if err := st.SaveRegistration(ctx, again); err != nil {
		t.Fatal(err)
	}
	mine, err := st.MemberRegistration(ctx, future, "anna.andersson")
	if err != nil {
		t.Fatal(err)
	}
	if mine.Adults != 3 {
		t.Errorf("Adults = %d, want the second answer to have replaced the first", mine.Adults)
	}
}

// A database from before the Mattermost switch has cooking teams with an
// e-mail address and no username. The teams have to survive the upgrade — and
// the address has to go, because nothing can be sent to it any more.
func TestMigrationFromTheTeamLeaderAddresses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
		CREATE TABLE teams (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			name         TEXT NOT NULL,
			leader_name  TEXT NOT NULL DEFAULT '',
			leader_email TEXT NOT NULL DEFAULT '',
			position     INTEGER NOT NULL DEFAULT 0,
			active       INTEGER NOT NULL DEFAULT 1
		);
		INSERT INTO teams (name, leader_name, leader_email, position, active)
		VALUES ('Lag 1', 'Anna Andersson', 'anna@example.se', 0, 1),
		       ('Lag 2', 'Bo Bengtsson',   'bo@example.se',   1, 0);
	`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("opening an old database: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	teams, err := st.Teams(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 2 {
		t.Fatalf("got %d teams after the upgrade, want 2", len(teams))
	}
	if teams[0].Name != "Lag 1" || teams[0].LeaderName != "Anna Andersson" || !teams[0].Active {
		t.Errorf("first team = %+v", teams[0])
	}
	// Nobody has said which account Anna is, so there is nobody to tell yet.
	if teams[0].LeaderUsername != "" {
		t.Errorf("LeaderUsername = %q, want it empty until an administrator fills it in",
			teams[0].LeaderUsername)
	}
	if teams[1].Active {
		t.Error("the second team was switched off and should have stayed off")
	}

	// The addresses are gone rather than sitting in the table unused.
	var n int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('teams') WHERE name = 'leader_email'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("leader_email is still in the teams table")
	}

	// Saving a username works, and reopening is a no-op rather than a second
	// migration.
	if _, err := st.SaveTeam(ctx, Team{
		ID: teams[0].ID, Name: "Lag 1", LeaderName: "Anna Andersson",
		LeaderUsername: "anna.andersson", Active: true,
	}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer again.Close()
	teams, _ = again.Teams(ctx)
	if len(teams) != 2 || teams[0].LeaderUsername != "anna.andersson" {
		t.Errorf("teams after reopening = %+v", teams)
	}
}

// ------------------------------------------------------- regular guests --

// standing builds a regular guest's row for one weekday of one request.
func guestStanding(token string, wd time.Weekday, name string, adults int) Standing {
	return Standing{
		ID: "id-" + token + "-" + wd.String(), Kind: KindGuest, Token: token,
		Weekday: wd, Name: name, Host: "Anna Andersson", Adults: adults,
		Diet: DietOmnivore, Status: StatusPending, CreatedAt: now, UpdatedAt: now,
	}
}

func TestARegularsRequestIsOneThingFoundByItsLink(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	for _, wd := range []time.Weekday{time.Tuesday, time.Thursday} {
		if err := st.SaveStanding(ctx, guestStanding("tok", wd, "Kalle", 2)); err != nil {
			t.Fatal(err)
		}
	}
	// A household's own, on one of the same evenings, is a separate thing.
	if err := st.SaveStanding(ctx, Standing{
		ID: "s1", Member: "anna.andersson", Weekday: time.Tuesday,
		Name: "Anna", Adults: 2, Diet: DietOmnivore, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := st.StandingByToken(ctx, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("StandingByToken = %d rows, want one per evening asked for", len(rows))
	}
	if rows[0].Host != "Anna Andersson" || rows[0].Kind != KindGuest {
		t.Errorf("stored %+v", rows[0])
	}
	if rows[0].Counts() {
		t.Error("a request nobody has agreed to must not count")
	}

	// One request, however many evenings it covers.
	if n, err := st.PendingStandingCount(ctx); err != nil || n != 1 {
		t.Errorf("PendingStandingCount = %d (%v), want 1", n, err)
	}
	if waiting, _ := st.PendingStanding(ctx); len(waiting) != 2 {
		t.Errorf("PendingStanding = %d rows, want both evenings", len(waiting))
	}
	// And the household's own is neither in the queue nor found by a link.
	if list, _ := st.StandingByMember(ctx, "anna.andersson"); len(list) != 1 {
		t.Errorf("the household's own standing registration = %+v", list)
	}

	approved := now.Add(48 * time.Hour)
	if err := st.ApproveStanding(ctx, "tok", approved); err != nil {
		t.Fatal(err)
	}
	rows, _ = st.StandingByToken(ctx, "tok")
	for _, row := range rows {
		if !row.Counts() {
			t.Errorf("%s should count once approved", row.Weekday)
		}
		// The approval is what decides which evenings it was in force for, so
		// it is the moment the row is dated from.
		if !row.UpdatedAt.Equal(approved) {
			t.Errorf("UpdatedAt = %s, want the approval at %s", row.UpdatedAt, approved)
		}
		if !row.CreatedAt.Equal(now) {
			t.Errorf("CreatedAt = %s, want when it was asked for", row.CreatedAt)
		}
	}
	if n, _ := st.PendingStandingCount(ctx); n != 0 {
		t.Errorf("nothing should be waiting any more, got %d", n)
	}

	// Withdrawing takes the whole request with it, and nothing else.
	if err := st.DeleteStandingByToken(ctx, "tok"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.StandingByToken(ctx, "tok"); len(rows) != 0 {
		t.Errorf("the request should be gone, got %+v", rows)
	}
	if list, _ := st.StandingByMember(ctx, "anna.andersson"); len(list) != 1 {
		t.Error("the household's own standing registration was taken with it")
	}
	if err := st.DeleteStandingByToken(ctx, "tok"); !errors.Is(err, ErrNotFound) {
		t.Errorf("withdrawing twice = %v, want ErrNotFound", err)
	}
}

// The old unique index was over (member, weekday), which two regular guests —
// who have no account between them — would have collided on.
func TestTwoRegularsCanEatOnTheSameEvening(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	for _, who := range []struct{ token, name string }{{"a", "Kalle"}, {"b", "Stina"}} {
		if err := st.SaveStanding(ctx, guestStanding(who.token, time.Tuesday, who.name, 1)); err != nil {
			t.Fatalf("%s: %v", who.name, err)
		}
	}
	rows, err := st.StandingFor(ctx, time.Tuesday)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("StandingFor(Tuesday) = %d rows, want both regulars", len(rows))
	}

	// A second row for the same evening of the same request is the same row.
	again := guestStanding("a", time.Tuesday, "Kalle", 4)
	again.ID = "another-id"
	if err := st.SaveStanding(ctx, again); err != nil {
		t.Fatal(err)
	}
	rows, _ = st.StandingByToken(ctx, "a")
	if len(rows) != 1 || rows[0].Adults != 4 {
		t.Errorf("saving again should have replaced it, got %+v", rows)
	}
}

// A regular guest's answer for one evening is an exception to their standing
// registration, and editing it must not leave two of them behind.
func TestAnExceptionReplacesTheRegularsEarlierAnswer(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	first := Registration{
		ID: "e1", Date: "2026-08-25", Kind: KindGuest, Name: "Kalle",
		Host: "Anna", Adults: 3, Diet: DietOmnivore, StandingToken: "tok",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.SaveRegistration(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID, second.Adults = "e2", 0
	if err := st.SaveRegistration(ctx, second); err != nil {
		t.Fatal(err)
	}

	regs, err := st.Registrations(ctx, "2026-08-25")
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 {
		t.Fatalf("got %d rows, want one answer per evening per regular", len(regs))
	}
	if regs[0].ID != "e1" {
		t.Errorf("ID = %q, want the row to keep its identity", regs[0].ID)
	}
	if regs[0].Attending() {
		t.Error("the later answer said nobody is coming")
	}

	got, err := st.StandingException(ctx, "2026-08-25", "tok")
	if err != nil {
		t.Fatalf("StandingException: %v", err)
	}
	if got.ID != "e1" {
		t.Errorf("StandingException found %+v", got)
	}
	// A one-off visitor's registration is not an exception to anything, and an
	// exception is not reachable by a link it does not have.
	if _, err := st.StandingException(ctx, "2026-08-25", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty link = %v, want ErrNotFound", err)
	}
	if _, err := st.RegistrationByToken(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty token = %v, want ErrNotFound", err)
	}
}

// The build before this one keyed every standing registration to a Mattermost
// account and had a unique index to match. A regular guest has no account, so
// the index has to be narrowed to the households before one can be stored at
// all.
func TestMigrationFromStandingRegistrationsWithoutRegulars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
		CREATE TABLE registrations (
			id TEXT PRIMARY KEY, date TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'member',
			member TEXT NOT NULL DEFAULT '', mm_user_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL, apartment TEXT NOT NULL DEFAULT '',
			host TEXT NOT NULL DEFAULT '',
			adults INTEGER NOT NULL DEFAULT 0, children INTEGER NOT NULL DEFAULT 0,
			diet TEXT NOT NULL DEFAULT 'allatare',
			note TEXT NOT NULL DEFAULT '', token TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			created_ip TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE standing (
			id TEXT PRIMARY KEY, member TEXT NOT NULL,
			mm_user_id TEXT NOT NULL DEFAULT '', weekday INTEGER NOT NULL,
			name TEXT NOT NULL, apartment TEXT NOT NULL DEFAULT '',
			adults INTEGER NOT NULL DEFAULT 0, children INTEGER NOT NULL DEFAULT 0,
			diet TEXT NOT NULL DEFAULT 'allatare',
			note TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
		);
		CREATE UNIQUE INDEX idx_standing ON standing (member, weekday);
		INSERT INTO standing (id, member, mm_user_id, weekday, name, adults, updated_at)
		VALUES ('s1', 'anna.andersson', 'u1', 2, 'Anna', 2, '2026-01-01T10:00:00Z');
	`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("opening the previous build's database: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// The household's own is still there, still counted, and nobody had to
	// approve it.
	list, err := st.StandingByMember(ctx, "anna.andersson")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Adults != 2 {
		t.Fatalf("the household's standing registration = %+v", list)
	}
	if !list[0].Counts() {
		t.Error("what was already in force must not need approving")
	}
	if list[0].Guest() {
		t.Error("what was there is a household's own")
	}

	// And two regular guests fit on the evening it is on.
	for _, token := range []string{"a", "b"} {
		if err := st.SaveStanding(ctx, guestStanding(token, time.Tuesday, "Kalle", 1)); err != nil {
			t.Fatalf("%s: %v", token, err)
		}
	}
	if rows, _ := st.StandingFor(ctx, time.Tuesday); len(rows) != 3 {
		t.Errorf("StandingFor(Tuesday) = %d rows, want the household and both regulars", len(rows))
	}
}
