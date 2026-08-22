package setup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/O5ten/dinners/internal/config"
	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/mattermost"
	"github.com/O5ten/dinners/internal/store"
)

const withSeed = `
site:
  title: Test
  timezone: Europe/Stockholm
dinner:
  weekdays: [tisdag, torsdag]
deadline:
  weekday: fredag
  time: "23:59"
  weeks_before: 1
teams:
  - name: Lag 1
    mattermost: "@Anna.Andersson"
  - name: Lag 2
    mattermost: bo.bengtsson
season:
  name: Hösten 2026
  start: 2026-08-25
  end: 2026-12-17
`

func load(t *testing.T, body string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// fakeDirectory stands in for the house's chat: it knows two people, and how
// they spell their own names.
type fakeDirectory map[string]mattermost.User

func (d fakeDirectory) ByUsername(_ context.Context, username string) (mattermost.User, error) {
	u, ok := d[username]
	if !ok {
		return mattermost.User{}, fmt.Errorf("no such user %q", username)
	}
	return u, nil
}

var houseChat = fakeDirectory{
	"anna.andersson": {ID: "u-anna", Username: "anna.andersson", FirstName: "Anna", LastName: "Andersson"},
	"bo.bengtsson":   {ID: "u-bo", Username: "bo.bengtsson", Nickname: "Bosse"},
}

func open(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestBootstrapSeedsAnEmptyDatabase(t *testing.T) {
	st, ctx := open(t), context.Background()
	cfg := load(t, withSeed)

	seeded, err := Bootstrap(ctx, st, cfg, houseChat)
	if err != nil || !seeded {
		t.Fatalf("Bootstrap = %v, %v", seeded, err)
	}

	teams, _ := st.Teams(ctx)
	if len(teams) != 2 {
		t.Fatalf("got %d teams, want 2", len(teams))
	}
	if teams[0].Name != "Lag 1" || teams[0].Position != 0 || !teams[0].Active {
		t.Errorf("first team = %+v", teams[0])
	}
	// The username is what the bot addresses, so a pasted "@Anna.Andersson"
	// has to be stored the way Mattermost spells it.
	if teams[0].LeaderUsername != "anna.andersson" {
		t.Errorf("LeaderUsername = %q, want it lower-cased and without the @",
			teams[0].LeaderUsername)
	}
	// And nobody writes the leader's name: it comes from their account, which
	// is also where a nickname comes from when that is all the account has.
	if teams[0].LeaderName != "Anna Andersson" {
		t.Errorf("LeaderName = %q, want the name on the account", teams[0].LeaderName)
	}
	if teams[1].LeaderName != "Bosse" {
		t.Errorf("second LeaderName = %q, want the nickname on the account", teams[1].LeaderName)
	}

	seasons, _ := st.Seasons(ctx)
	if len(seasons) != 1 || seasons[0].Start != "2026-08-25" {
		t.Fatalf("seasons = %+v", seasons)
	}
	if len(seasons[0].Weekdays) != 2 {
		t.Errorf("the season should inherit the dinner weekdays, got %v", seasons[0].Weekdays)
	}

	set, _ := st.Settings(ctx, store.Settings{})
	if set.DeadlineWeekday != time.Friday || set.DeadlineMinutes != 23*60+59 {
		t.Errorf("settings = %+v", set)
	}
	if !set.GuestOpen {
		t.Error("the guest page should start out open")
	}
}

// The YAML is a starting point, not a source of truth: once the matgrupp has
// edited anything, a later config change must not undo it.
func TestBootstrapNeverTouchesAnUsedDatabase(t *testing.T) {
	st, ctx := open(t), context.Background()
	cfg := load(t, withSeed)

	if _, err := Bootstrap(ctx, st, cfg, houseChat); err != nil {
		t.Fatal(err)
	}
	teams, _ := st.Teams(ctx)
	teams[0].Name = "Omdöpt av matgruppen"
	if _, err := st.SaveTeam(ctx, teams[0]); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTeam(ctx, teams[1].ID); err != nil {
		t.Fatal(err)
	}

	seeded, err := Bootstrap(ctx, st, cfg, houseChat)
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Error("Bootstrap should report that it did nothing")
	}
	after, _ := st.Teams(ctx)
	if len(after) != 1 || after[0].Name != "Omdöpt av matgruppen" {
		t.Errorf("the second run changed things: %+v", after)
	}
}

// A username the chat server does not know, or no chat server at all, must not
// stop a first start: the team is seeded with what the file says and gets its
// name the first time an administrator saves it.
func TestBootstrapSurvivesALeaderTheChatDoesNotKnow(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  Directory
	}{
		{"a stranger", houseChat},
		{"no chat server at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, ctx := open(t), context.Background()
			cfg := load(t, "site:\n  title: Test\nteams:\n  - name: Lag 1\n    mattermost: hittepa.person\n")
			if _, err := Bootstrap(ctx, st, cfg, tc.dir); err != nil {
				t.Fatalf("Bootstrap: %v", err)
			}
			teams, _ := st.Teams(ctx)
			if len(teams) != 1 || teams[0].LeaderUsername != "hittepa.person" {
				t.Fatalf("teams = %+v", teams)
			}
			if teams[0].LeaderName != "" {
				t.Errorf("LeaderName = %q, want nothing invented", teams[0].LeaderName)
			}
		})
	}
}

func TestBootstrapWithoutTeamsOrSeason(t *testing.T) {
	st, ctx := open(t), context.Background()
	cfg := load(t, "site:\n  title: Test\n")
	if _, err := Bootstrap(ctx, st, cfg, houseChat); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if teams, _ := st.Teams(ctx); len(teams) != 0 {
		t.Errorf("got %d teams", len(teams))
	}
	if seasons, _ := st.Seasons(ctx); len(seasons) != 0 {
		t.Errorf("got %d seasons", len(seasons))
	}
	// Settings are still written, so the deadline is defined from the start.
	if stored, _ := st.SettingsStored(ctx); !stored {
		t.Error("settings should have been seeded")
	}
}

// The demo is the first thing a newcomer runs, so it has to produce a house
// that already looks lived-in.
func TestDemoProducesAUsableHouse(t *testing.T) {
	st, ctx := open(t), context.Background()
	cfg := load(t, "site:\n  title: Test\n")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

	n, err := Demo(ctx, st, cfg, now)
	if err != nil {
		t.Fatalf("Demo: %v", err)
	}
	if n == 0 {
		t.Fatal("no registrations were seeded")
	}

	teams, _ := st.Teams(ctx)
	if len(teams) != 4 {
		t.Errorf("got %d teams, want the four cooking teams", len(teams))
	}
	for _, team := range teams {
		if team.LeaderUsername == "" {
			t.Errorf("%s has no leader username, so it could never be told anything", team.Name)
		}
	}
	seasons, _ := st.Seasons(ctx)
	if len(seasons) != 1 {
		t.Fatalf("got %d seasons", len(seasons))
	}
	if len(seasons[0].Weekdays) != 2 {
		t.Errorf("weekdays = %v", seasons[0].Weekdays)
	}
	if breaks, _ := st.Breaks(ctx); len(breaks) != 1 {
		t.Errorf("the demo should show one break, got %d", len(breaks))
	}
	if standing, _ := st.AllStanding(ctx); len(standing) == 0 {
		t.Error("the demo should show standing registrations")
	}

	// The season must straddle today, with dinners both behind and ahead.
	overrides, _ := st.Overrides(ctx, "0000-01-01", "9999-12-31")
	breaks, _ := st.Breaks(ctx)
	sched := dinner.Build(seasons, teams, overrides, breaks, dinner.Params{
		Loc: cfg.Location(), ServingMinutes: 18 * 60,
		Deadline: dinner.Deadline{Weekday: time.Friday, Minutes: 23*60 + 59, WeeksBefore: 1},
	})
	var past, open int
	for _, d := range sched.Dinners {
		switch {
		case d.Over(now):
			past++
		case d.Open(now):
			open++
		}
	}
	if past == 0 {
		t.Error("the demo should have dinners that already happened")
	}
	if open == 0 {
		t.Error("the demo should have a dinner you can still register for")
	}

	// Everything that closed has already been mailed, so the admin schedule
	// does not open on a pile of missed notifications.
	for _, d := range sched.Dinners {
		if d.Cancelled || !now.After(d.Closes) {
			continue
		}
		if done, _ := st.Notified(ctx, d.Key, "deadline"); !done {
			t.Errorf("%s closed before now but was never marked as mailed", d.Key)
		}
	}
}

func TestDemoLeavesARealDatabaseAlone(t *testing.T) {
	st, ctx := open(t), context.Background()
	cfg := load(t, "site:\n  title: Test\n")
	if _, err := st.SaveTeam(ctx, store.Team{Name: "Riktigt lag", Active: true}); err != nil {
		t.Fatal(err)
	}
	n, err := Demo(ctx, st, cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("Demo seeded %d rows into a database in use", n)
	}
	if teams, _ := st.Teams(ctx); len(teams) != 1 {
		t.Errorf("teams = %+v", teams)
	}
}

// Seeding twice in a row — a restarted demo container — must not pile example
// data on top of itself.
func TestDemoIsIdempotent(t *testing.T) {
	st, ctx := open(t), context.Background()
	cfg := load(t, "site:\n  title: Test\n")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	first, err := Demo(ctx, st, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Demo(ctx, st, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	if second != 0 {
		t.Errorf("the second seed added %d rows, want 0 (first added %d)", second, first)
	}
}
