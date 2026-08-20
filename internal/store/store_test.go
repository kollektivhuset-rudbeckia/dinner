package store

import (
	"context"
	"database/sql"
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

func reg(date, email, name string, adults, children int) Registration {
	return Registration{
		ID: "id-" + email + "-" + date, Date: date, Kind: KindMember,
		Email: email, Name: name, Adults: adults, Children: children,
		Token: "tok-" + email + "-" + date, CreatedAt: now, UpdatedAt: now,
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

	first := reg("2026-08-25", "anna@x.se", "Anna", 2, 1)
	if err := st.SaveRegistration(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := reg("2026-08-25", "anna@x.se", "Anna Andersson", 1, 0)
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

// Two visitors may share an address, or leave it out; neither may collide.
func TestGuestRegistrationsAreAlwaysSeparateRows(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	for i, name := range []string{"Kalle", "Maja"} {
		r := Registration{
			ID: "g" + string(rune('0'+i)), Date: "2026-08-25", Kind: KindGuest,
			Name: name, Email: "", Adults: 1,
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
		Adults: 2, Token: "secret-token", CreatedAt: now, UpdatedAt: now}
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
		reg("2026-08-25", "anna@x.se", "Anna", 2, 0),
		reg("2026-08-27", "anna@x.se", "Anna", 1, 0),
		reg("2026-09-01", "bo@x.se", "Bo", 1, 0),
	} {
		if err := st.SaveRegistration(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.MemberRegistration(ctx, "2026-08-27", "anna@x.se")
	if err != nil || got.Adults != 1 {
		t.Errorf("MemberRegistration = %+v, %v", got, err)
	}
	if _, err := st.MemberRegistration(ctx, "2026-08-27", "bo@x.se"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}

	between, err := st.RegistrationsBetween(ctx, "2026-08-25", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(between) != 2 {
		t.Errorf("got %d in range, want 2", len(between))
	}

	mine, err := st.MemberRegistrations(ctx, "anna@x.se", "2026-08-26")
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
	r := reg("2026-08-25", "anna@x.se", "Anna", 1, 0)
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
	base := Standing{ID: "s1", Email: "anna@x.se", Weekday: time.Tuesday,
		Name: "Anna", Adults: 2, Children: 1, UpdatedAt: now}
	if err := st.SaveStanding(ctx, base); err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.ID, changed.Adults = "s2", 3
	if err := st.SaveStanding(ctx, changed); err != nil {
		t.Fatal(err)
	}

	list, err := st.StandingByEmail(ctx, "anna@x.se")
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
	if list, _ := st.StandingByEmail(ctx, "anna@x.se"); len(list) != 0 {
		t.Errorf("saving nobody should have cleared it, got %+v", list)
	}
}

func TestStandingIsPerWeekday(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	for _, wd := range []time.Weekday{time.Tuesday, time.Thursday} {
		if err := st.SaveStanding(ctx, Standing{
			ID: "s" + wd.String(), Email: "anna@x.se", Weekday: wd,
			Name: "Anna", Adults: 1, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := st.StandingByEmail(ctx, "anna@x.se"); len(list) != 2 {
		t.Errorf("got %d rows, want one per weekday", len(list))
	}
	tue, err := st.StandingFor(ctx, time.Tuesday)
	if err != nil {
		t.Fatal(err)
	}
	if len(tue) != 1 {
		t.Errorf("StandingFor(Tuesday) = %d rows, want 1", len(tue))
	}
	if err := st.DeleteStanding(ctx, "anna@x.se", time.Tuesday); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.StandingByEmail(ctx, "anna@x.se"); len(list) != 1 {
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
	if err := st.MarkNotified(ctx, "2026-08-25", "deadline", "anna@x.se", now); err != nil {
		t.Fatal(err)
	}
	if done, _ := st.Notified(ctx, "2026-08-25", "deadline"); !done {
		t.Error("should be recorded")
	}
	if err := st.MarkNotified(ctx, "2026-08-25", "deadline", "bo@x.se", now); err == nil {
		t.Error("a second record for the same dinner must be refused")
	}

	sent, err := st.SentNotifications(ctx, "2026-08-01", "2026-08-31")
	if err != nil {
		t.Fatal(err)
	}
	log, ok := sent["2026-08-25"]
	if !ok || !log.SentAt.Equal(now) || log.Recipient != "anna@x.se" {
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
