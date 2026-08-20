package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/O5ten/dinners/internal/config"
	"github.com/O5ten/dinners/internal/dinner"
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
    leader: Anna
    email: Anna@Example.SE
  - name: Lag 2
    leader: Bo
    email: bo@example.se
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

	seeded, err := Bootstrap(ctx, st, cfg)
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
	// Addresses are the identifier the mail goes to; they must be normalised.
	if teams[0].LeaderEmail != "anna@example.se" {
		t.Errorf("LeaderEmail = %q, want it lower-cased", teams[0].LeaderEmail)
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

	if _, err := Bootstrap(ctx, st, cfg); err != nil {
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

	seeded, err := Bootstrap(ctx, st, cfg)
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

func TestBootstrapWithoutTeamsOrSeason(t *testing.T) {
	st, ctx := open(t), context.Background()
	cfg := load(t, "site:\n  title: Test\n")
	if _, err := Bootstrap(ctx, st, cfg); err != nil {
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
		if team.LeaderEmail == "" {
			t.Errorf("%s has no leader address, so it could never be mailed", team.Name)
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
