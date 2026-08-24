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
	"github.com/O5ten/dinners/internal/mattermost"
	"github.com/O5ten/dinners/internal/store"
)

// Directory is the part of the Mattermost bot the first-run seeding needs: the
// leaders in config.yaml are named by username, and their own spelling of
// their name comes from their account.
type Directory interface {
	ByUsername(ctx context.Context, username string) (mattermost.User, error)
}

// Bootstrap seeds an empty database from config.yaml, and does nothing at all
// once anything has been written. The YAML is a starting point, not a source
// of truth: after the first start the matgrupp edits teams and seasons in the
// admin view, and a later config change must not quietly undo that.
//
// dir looks the cooking teams' leaders up in the house's chat. It may be a
// disabled client, or nil, in which case a team keeps its username and no
// name until an administrator saves it from the admin view.
func Bootstrap(ctx context.Context, st *store.Store, cfg *config.Config, dir Directory) (bool, error) {
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
		username := mattermost.Username(t.Mattermost)
		if _, err := st.SaveTeam(ctx, store.Team{
			Name:           t.Name,
			LeaderName:     leaderName(ctx, dir, username),
			LeaderUsername: username,
			Position:       i,
			Active:         true,
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
	// Member is the household's Mattermost username. The demo never reaches a
	// chat server, so these are names in a database and nothing more.
	Member   string
	Adults   int
	Children int
	Diet     store.Diet
	Note     string
	// Standing says which evenings this household eats by default: an empty
	// list means they answer one dinner at a time.
	Standing []time.Weekday
}

// The cast is fictional. The demo runs with the bot switched off, so none of
// these usernames is ever looked up or written to.
var households = []household{
	{Name: "Anna Andersson", Apartment: "1403", Member: "anna.andersson",
		Adults: 2, Children: 2, Diet: store.DietOmnivore,
		Note:     "ett barn tål inte gluten",
		Standing: []time.Weekday{time.Tuesday, time.Thursday}},
	{Name: "Bo Bengtsson", Apartment: "0702", Member: "bo.bengtsson",
		Adults: 1, Diet: store.DietVegetarian,
		Standing: []time.Weekday{time.Tuesday}},
	{Name: "Cecilia Dahl", Apartment: "1201", Member: "cecilia.dahl",
		Adults: 2, Children: 1, Diet: store.DietVegan,
		Note:     "inga nötter, tack",
		Standing: []time.Weekday{time.Thursday}},
	{Name: "David Ek", Apartment: "0304", Member: "david.ek",
		Adults: 1, Diet: store.DietPescetarian},
	{Name: "Elin Forsberg", Apartment: "0508", Member: "elin.forsberg",
		Adults: 2, Children: 3, Diet: store.DietVegetarian,
		Standing: []time.Weekday{time.Tuesday, time.Thursday}},
	{Name: "Farid Hassan", Apartment: "1105", Member: "farid.hassan",
		Adults: 2, Diet: store.DietFlexitarian, Note: "fläskfritt"},
	{Name: "Greta Lind", Apartment: "0601", Member: "greta.lind",
		Adults: 1, Diet: store.DietVegan,
		Standing: []time.Weekday{time.Thursday}},
	{Name: "Hugo Nyström", Apartment: "0907", Member: "hugo.nystrom",
		Adults: 2, Children: 1, Diet: store.DietOmnivore},
	{Name: "Ingrid Palm", Apartment: "1302", Member: "ingrid.palm",
		Adults: 1, Diet: store.DietFlexitarian},
	{Name: "Jonas Rehn", Apartment: "0203", Member: "jonas.rehn",
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

// regulars are friends and family of the house who eat here every week without
// being in its Mattermost: one already approved, so the lists show what that
// looks like, and one still waiting, so the admin view has a request in the
// queue. The waiting one names nobody in the house, which is the other case an
// administrator has to judge.
var regulars = []struct {
	Name     string
	Host     string
	Adults   int
	Children int
	Diet     store.Diet
	Note     string
	Weekdays []time.Weekday
	Status   store.Status
}{
	{"Tove Kihlberg", "Cecilia Dahl", 1, 0, store.DietVegan, "",
		[]time.Weekday{time.Thursday}, store.StatusApproved},
	{"Rune Ahlberg", "", 2, 0, store.DietOmnivore, "inga svampar",
		[]time.Weekday{time.Tuesday, time.Thursday}, store.StatusPending},
}

// demoTeams are made up, and so are their usernames and leaders: the demo
// never reaches a chat server, so it writes the names a lookup would otherwise
// have brought back, and nobody is to be messaged from it by accident.
var demoTeams = []struct{ Name, Leader, Username string }{
	{"Lag 1", "Anna Andersson", "anna.andersson"},
	{"Lag 2", "Bo Bengtsson", "bo.bengtsson"},
	{"Lag 3", "Cecilia Dahl", "cecilia.dahl"},
	{"Lag 4", "David Ek", "david.ek"},
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
			Name: t.Name, LeaderName: t.Leader, LeaderUsername: t.Username,
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
				ID: auth.ID(), Member: h.Member, Weekday: wd, Name: h.Name,
				Apartment: h.Apartment, Adults: h.Adults, Children: h.Children,
				Diet: h.Diet, Note: h.Note,
				UpdatedAt: now,
			}); err != nil {
				return 0, err
			}
		}
	}

	// The regulars. One request per person, however many evenings it covers,
	// which is what an administrator says yes or no to in one go.
	for _, g := range regulars {
		token := auth.Token()
		for _, wd := range g.Weekdays {
			if err := st.SaveStanding(ctx, store.Standing{
				ID: auth.ID(), Kind: store.KindGuest, Token: token, Weekday: wd,
				Name: g.Name, Host: g.Host,
				Adults: g.Adults, Children: g.Children,
				Diet: g.Diet, Note: g.Note, Status: g.Status,
				// Long in force, so the approved one is on the lists that have
				// already gone out rather than only the evenings to come.
				CreatedAt: now.AddDate(0, 0, -30), UpdatedAt: now.AddDate(0, 0, -30),
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

	// Dinners whose deadline has passed already had their list sent, so the
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
			"demo.matlag", closes); err != nil {
			return n, err
		}
	}
	return n, nil
}

func saveReg(ctx context.Context, st *store.Store, date string, h household, now time.Time, adults, children int) error {
	r := store.Registration{
		ID: auth.ID(), Date: date, Kind: store.KindMember, Member: h.Member,
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

// leaderName asks the chat server how a leader spells their own name. A
// username that answers to nobody — or no chat server at all — is not worth
// failing a first start over: the team is seeded with its username, and the
// name fills itself in the first time the team is saved in the admin view.
func leaderName(ctx context.Context, dir Directory, username string) string {
	if dir == nil || username == "" {
		return ""
	}
	u, err := dir.ByUsername(ctx, username)
	if err != nil || u.ID == "" {
		return ""
	}
	return u.DisplayName()
}
