// Package setup gets a fresh database into a usable state: the first-run
// bootstrap that copies config.yaml's teams and season into the tables the
// admin view owns from then on, and the throwaway example data behind -demo.
package setup

import (
	"context"
	"fmt"
	"time"

	"github.com/O5ten/dinners/internal/auth"
	"github.com/O5ten/dinners/internal/config"
	"github.com/O5ten/dinners/internal/store"
)

// Bootstrap seeds an empty database from config.yaml, and does nothing at all
// once anything has been written. The YAML is a starting point, not a source
// of truth: after the first start the matgrupp edits teams and seasons in the
// admin view, and a later config change must not quietly undo that.
func Bootstrap(ctx context.Context, st *store.Store, cfg *config.Config) (bool, error) {
	empty, err := st.Empty(ctx)
	if err != nil {
		return false, err
	}
	if !empty {
		return false, nil
	}

	if err := st.SaveSettings(ctx, store.Settings{
		DeadlineWeekday:     cfg.Deadline.ParsedWeekday(),
		DeadlineMinutes:     cfg.Deadline.Minutes(),
		DeadlineWeeksBefore: cfg.Deadline.WeeksBefore,
		GuestOpen:           true,
	}); err != nil {
		return false, fmt.Errorf("seed settings: %w", err)
	}

	for i, t := range cfg.Teams {
		if _, err := st.SaveTeam(ctx, store.Team{
			Name:        t.Name,
			LeaderName:  t.Leader,
			LeaderEmail: auth.NormalizeEmail(t.Email),
			Position:    i,
			Active:      true,
		}); err != nil {
			return false, fmt.Errorf("seed team %q: %w", t.Name, err)
		}
	}

	if cfg.Season != nil {
		if _, err := st.SaveSeason(ctx, store.Season{
			Name:     cfg.Season.Name,
			Start:    cfg.Season.Start,
			End:      cfg.Season.End,
			Weekdays: cfg.Dinner.ParsedWeekdays(),
		}); err != nil {
			return false, fmt.Errorf("seed season: %w", err)
		}
	}
	return true, nil
}

// household is a made-up neighbour who eats with the others.
type household struct {
	Name      string
	Apartment string
	Email     string
	Adults    int
	Children  int
	Diet      store.Diet
	Note      string
	// Standing says which evenings this household eats by default: an empty
	// list means they answer one dinner at a time.
	Standing []time.Weekday
}

// The cast is fictional, and the addresses use the reserved example.com domain
// so nothing can accidentally be mailed to a real person.
var households = []household{
	{Name: "Anna Andersson", Apartment: "1403", Email: "anna@example.com",
		Adults: 2, Children: 2, Diet: store.DietOmnivore,
		Note:     "ett barn tål inte gluten",
		Standing: []time.Weekday{time.Tuesday, time.Thursday}},
	{Name: "Bo Bengtsson", Apartment: "0702", Email: "bo@example.com",
		Adults: 1, Diet: store.DietVegetarian,
		Standing: []time.Weekday{time.Tuesday}},
	{Name: "Cecilia Dahl", Apartment: "1201", Email: "cecilia@example.com",
		Adults: 2, Children: 1, Diet: store.DietVegan,
		Note:     "inga nötter, tack",
		Standing: []time.Weekday{time.Thursday}},
	{Name: "David Ek", Apartment: "0304", Email: "david@example.com",
		Adults: 1, Diet: store.DietPescetarian},
	{Name: "Elin Forsberg", Apartment: "0508", Email: "elin@example.com",
		Adults: 2, Children: 3, Diet: store.DietVegetarian,
		Standing: []time.Weekday{time.Tuesday, time.Thursday}},
	{Name: "Farid Hassan", Apartment: "1105", Email: "farid@example.com",
		Adults: 2, Diet: store.DietFlexitarian, Note: "fläskfritt"},
	{Name: "Greta Lind", Apartment: "0601", Email: "greta@example.com",
		Adults: 1, Diet: store.DietVegan,
		Standing: []time.Weekday{time.Thursday}},
	{Name: "Hugo Nyström", Apartment: "0907", Email: "hugo@example.com",
		Adults: 2, Children: 1, Diet: store.DietOmnivore},
	{Name: "Ingrid Palm", Apartment: "1302", Email: "ingrid@example.com",
		Adults: 1, Diet: store.DietFlexitarian},
	{Name: "Jonas Rehn", Apartment: "0203", Email: "jonas@example.com",
		Adults: 2, Children: 2, Diet: store.DietOmnivore,
		Note: "laktosfritt för en vuxen"},
}

var guests = []struct {
	Name string
	Host string
	Diet store.Diet
	Note string
}{
	{"Kalle Svensson", "Anna Andersson", store.DietOmnivore, ""},
	{"Maja Öberg", "David Ek", store.DietVegetarian, ""},
	{"Petra Lund", "Elin Forsberg", store.DietFlexitarian, ""},
	{"Sam Ali", "Hugo Nyström", store.DietPescetarian, "allergisk mot skaldjur"},
}

var demoTeams = []config.Team{
	{Name: "Lag 1", Leader: "Anna Andersson", Email: "anna@example.com"},
	{Name: "Lag 2", Leader: "Bo Bengtsson", Email: "bo@example.com"},
	{Name: "Lag 3", Leader: "Cecilia Dahl", Email: "cecilia@example.com"},
	{Name: "Lag 4", Leader: "David Ek", Email: "david@example.com"},
}

// Demo fills an empty database with a season that is already under way, so
// that someone trying the site out sees a house that eats together rather than
// an empty calendar. It is a no-op once the database holds anything.
func Demo(ctx context.Context, st *store.Store, cfg *config.Config, now time.Time) (int, error) {
	empty, err := st.Empty(ctx)
	if err != nil {
		return 0, err
	}
	if !empty {
		return 0, nil
	}
	loc := cfg.Location()
	now = now.In(loc)

	if err := st.SaveSettings(ctx, store.Settings{
		DeadlineWeekday:     cfg.Deadline.ParsedWeekday(),
		DeadlineMinutes:     cfg.Deadline.Minutes(),
		DeadlineWeeksBefore: cfg.Deadline.WeeksBefore,
		GuestOpen:           true,
	}); err != nil {
		return 0, err
	}
	for i, t := range demoTeams {
		if _, err := st.SaveTeam(ctx, store.Team{
			Name: t.Name, LeaderName: t.Leader, LeaderEmail: t.Email,
			Position: i, Active: true,
		}); err != nil {
			return 0, err
		}
	}

	// A season that started a few weeks ago and runs into the autumn, so the
	// schedule has both served dinners and ones still open for registration.
	weekdays := cfg.Dinner.ParsedWeekdays()
	start := monday(now, loc).AddDate(0, 0, -5*7)
	end := start.AddDate(0, 0, 18*7-1)
	if _, err := st.SaveSeason(ctx, store.Season{
		Name:     "Demosäsongen",
		Start:    start.Format("2006-01-02"),
		End:      end.Format("2006-01-02"),
		Weekdays: weekdays,
	}); err != nil {
		return 0, err
	}

	// A week off, far enough ahead that registration for it is still open, so
	// the demo shows what a school holiday does to the rotation.
	breakStart := monday(now, loc).AddDate(0, 0, 3*7)
	breakEnd := breakStart.AddDate(0, 0, 6)
	if _, err := st.SaveBreak(ctx, store.Break{
		Name:  "Höstlov",
		Start: breakStart.Format("2006-01-02"),
		End:   breakEnd.Format("2006-01-02"),
	}); err != nil {
		return 0, err
	}

	for _, h := range households {
		for _, wd := range h.Standing {
			if err := st.SaveStanding(ctx, store.Standing{
				ID: auth.ID(), Email: h.Email, Weekday: wd, Name: h.Name,
				Apartment: h.Apartment, Adults: h.Adults, Children: h.Children,
				Diet: h.Diet, Note: h.Note,
				UpdatedAt: now,
			}); err != nil {
				return 0, err
			}
		}
	}

	// Registrations for the dinners around today: everything from the start of
	// the season up to a month ahead, which is well past the deadline horizon.
	dates := dinnerDates(start, now.AddDate(0, 0, 30), weekdays, loc)
	n := 0
	// A tiny deterministic shuffle, so the demo looks lived-in but identical
	// every time it is seeded. (No rand: the same database twice is easier to
	// talk about in a bug report.)
	step := 0
	for _, day := range dates {
		key := day.Format("2006-01-02")
		// No dinner during the break, so no registrations for it either.
		if key >= breakStart.Format("2006-01-02") && key <= breakEnd.Format("2006-01-02") {
			continue
		}
		for i, h := range households {
			step++
			if hasWeekday(h.Standing, day.Weekday()) {
				// A household with a standing registration answers now and
				// then — usually to say it is skipping one evening.
				if step%11 == 0 {
					if err := saveReg(ctx, st, key, h, now, 0, 0); err != nil {
						return n, err
					}
					n++
				}
				continue
			}
			if (step+i)%3 == 0 {
				continue
			}
			if err := saveReg(ctx, st, key, h, now, h.Adults, h.Children); err != nil {
				return n, err
			}
			n++
		}
		// A guest every so often.
		if step%7 < 2 {
			g := guests[(step/7)%len(guests)]
			if err := st.SaveRegistration(ctx, store.Registration{
				ID: auth.ID(), Date: key, Kind: store.KindGuest,
				Name: g.Name, Host: g.Host, Adults: 1,
				Diet: g.Diet, Note: g.Note,
				Token: auth.Token(), CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return n, err
			}
			n++
		}
	}

	// One evening off and one with a message, so the schedule shows what an
	// exception looks like.
	if len(dates) > 4 {
		off := dates[len(dates)-3]
		if err := st.SaveOverride(ctx, store.Override{
			Date: off.Format("2006-01-02"), Cancelled: true,
			Note: "Inställd — festkommittén har lokalen.", UpdatedAt: now,
		}); err != nil {
			return n, err
		}
		party := dates[len(dates)-1]
		if err := st.SaveOverride(ctx, store.Override{
			Date:      party.Format("2006-01-02"),
			Note:      "Temakväll: soppa och bröd. Ta med egen skål om du kan.",
			UpdatedAt: now,
		}); err != nil {
			return n, err
		}
	}

	// Dinners whose deadline has passed already had their list mailed, so the
	// admin schedule does not look like a pile of missed notifications.
	deadline := demoDeadline(cfg)
	for _, day := range dates {
		key := day.Format("2006-01-02")
		if key >= breakStart.Format("2006-01-02") && key <= breakEnd.Format("2006-01-02") {
			continue
		}
		closes := deadlineFor(day, deadline, loc)
		if now.Before(closes) {
			continue
		}
		if err := st.MarkNotified(ctx, key, "deadline",
			"demo@example.com", closes); err != nil {
			return n, err
		}
	}
	return n, nil
}

func saveReg(ctx context.Context, st *store.Store, date string, h household, now time.Time, adults, children int) error {
	r := store.Registration{
		ID: auth.ID(), Date: date, Kind: store.KindMember, Email: h.Email,
		Name: h.Name, Apartment: h.Apartment,
		Adults: adults, Children: children,
		Token: auth.Token(), CreatedAt: now, UpdatedAt: now,
	}
	if adults+children > 0 {
		r.Diet, r.Note = h.Diet, h.Note
	}
	return st.SaveRegistration(ctx, r)
}

type deadlineRule struct {
	weekday time.Weekday
	minutes int
	weeks   int
}

func demoDeadline(cfg *config.Config) deadlineRule {
	return deadlineRule{
		weekday: cfg.Deadline.ParsedWeekday(),
		minutes: cfg.Deadline.Minutes(),
		weeks:   cfg.Deadline.WeeksBefore,
	}
}

// deadlineFor mirrors dinner.Deadline.For. It is repeated rather than imported
// so that seeding stays a leaf of the dependency graph.
func deadlineFor(day time.Time, d deadlineRule, loc *time.Location) time.Time {
	week := monday(day, loc).AddDate(0, 0, -7*d.weeks)
	t := week.AddDate(0, 0, (int(d.weekday)+6)%7)
	return time.Date(t.Year(), t.Month(), t.Day(), d.minutes/60, d.minutes%60, 0, 0, loc)
}

func monday(t time.Time, loc *time.Location) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

func dinnerDates(from, to time.Time, weekdays []time.Weekday, loc *time.Location) []time.Time {
	want := map[time.Weekday]bool{}
	for _, wd := range weekdays {
		want[wd] = true
	}
	var out []time.Time
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		if want[day.Weekday()] {
			out = append(out, day)
		}
	}
	return out
}

func hasWeekday(list []time.Weekday, wd time.Weekday) bool {
	for _, d := range list {
		if d == wd {
			return true
		}
	}
	return false
}
