package web

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/config"
	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/store"
)

// scheduleRow is one evening in the admin schedule, with everything the
// administrator needs to judge it at a glance.
type scheduleRow struct {
	Dinner  dinner.Dinner
	Summary dinner.Summary
	SentAt  time.Time
	Sent    bool
	// Unmanned marks an evening whose deadline passed with no cooking-team
	// leader to tell. Nothing was sent; the row says so rather than claiming
	// it was.
	Unmanned bool
	Open     bool
	Over     bool
	ListURL  string
}

// adminTabs are the admin view's pages, in the order the tab bar shows them.
// Splitting them up keeps each page to one job — and keeps the server from
// building a season's whole schedule when all you wanted was to fix a typo in
// a team name.
var adminTabs = []struct{ ID, Key string }{
	{"schema", "admin.tab.schedule"},
	{"lag", "admin.tab.teams"},
	{"sasonger", "admin.tab.seasons"},
	{"uppehall", "admin.tab.breaks"},
	{"installningar", "admin.tab.settings"},
	{"staende", "admin.tab.standing"},
}

// tab is one entry in the tab bar, with its name already translated.
type tab struct {
	ID   string
	Name string
}

func adminTab(raw string) string {
	for _, t := range adminTabs {
		if t.ID == raw {
			return t.ID
		}
	}
	return adminTabs[0].ID
}

// tabsFor names the tabs in the reader's language.
func tabsFor(lang i18n.Lang) []tab {
	out := make([]tab, 0, len(adminTabs))
	for _, t := range adminTabs {
		out = append(out, tab{ID: t.ID, Name: i18n.T(lang, t.Key)})
	}
	return out
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	v.GuestOpen = world.Settings.GuestOpen

	// Which season's schedule to show: the one asked for, else the one we are
	// in, else the next to start, else the last there was.
	var shown *store.Season
	if id := r.URL.Query().Get("sasong"); id != "" {
		n, _ := strconv.ParseInt(id, 10, 64)
		for i := range world.Seasons {
			if world.Seasons[i].ID == n {
				shown = &world.Seasons[i]
			}
		}
	}
	if shown == nil {
		shown = currentSeason(world, v.Now)
	}
	if shown == nil && len(world.Seasons) > 0 {
		shown = &world.Seasons[len(world.Seasons)-1]
	}

	tab := adminTab(r.URL.Query().Get("flik"))

	var rows []scheduleRow
	if shown != nil && tab == "schema" {
		var dinners []dinner.Dinner
		for _, d := range world.Schedule.Dinners {
			if d.Season.ID == shown.ID {
				dinners = append(dinners, d)
			}
		}
		summaries, err := s.summarize(ctx, dinners)
		if err != nil {
			s.fail(w, r, "summarize dinners", err)
			return
		}
		sent, err := s.store.SentNotifications(ctx, shown.Start, shown.End)
		if err != nil {
			s.fail(w, r, "read the notification log", err)
			return
		}
		for _, d := range dinners {
			log, logged := sent[d.Key]
			rows = append(rows, scheduleRow{
				Dinner: d, Summary: summaries[d.Key],
				SentAt:   log.SentAt.In(v.Loc),
				Sent:     logged && log.Recipient != noTeam,
				Unmanned: logged && log.Recipient == noTeam,
				Open:     d.Open(v.Now), Over: d.Over(v.Now),
				ListURL: "/middag/" + d.Key + "/lista",
			})
		}
	}

	var standing []store.Standing
	if tab == "staende" {
		standing, err = s.store.AllStanding(ctx)
		if err != nil {
			s.fail(w, r, "read standing registrations", err)
			return
		}
	}

	v.Title = i18n.T(v.Lang, "admin.title")
	v.Data = map[string]any{
		"Tab":      tab,
		"Tabs":     tabsFor(v.Lang),
		"Breaks":   world.Breaks,
		"Settings": world.Settings,
		"Teams":    world.Teams,
		"Seasons":  world.Seasons,
		"Season":   shown,
		"Rows":     rows,
		"Standing": standing,
		"Weekdays": allWeekdays(),
		// The years the date selectors offer.
		"Years":    yearOptions(world, v.Now),
		"GuestURL": s.rt.BaseURL + "/gast",
		"Saved":    r.URL.Query().Get("sparat"),
		"ChatOn":   s.mm.Enabled(),
		// The admin view is where the guest link and the spreadsheet formula
		// are read off the screen, so it is the right place to say that the
		// address in them is not the real one.
		"BaseURLUnset":     s.rt.BaseURLUnset() && !s.rt.Demo,
		"BaseURL":          s.rt.BaseURL,
		"NextSeasonOffset": nextRotationOffset(world),
	}
	s.render(w, r, http.StatusOK, "admin.html", v)
}

// allWeekdays lists Monday to Sunday for the season form.
func allWeekdays() []time.Weekday {
	return []time.Weekday{time.Monday, time.Tuesday, time.Wednesday,
		time.Thursday, time.Friday, time.Saturday, time.Sunday}
}

// nextRotationOffset suggests where a new season should start its rotation, so
// the teams carry on rather than the first one cooking twice in a row.
func nextRotationOffset(world *world) int {
	active := 0
	for _, t := range world.Teams {
		if t.Active {
			active++
		}
	}
	if active == 0 || len(world.Seasons) == 0 {
		return 0
	}
	last := world.Seasons[len(world.Seasons)-1]
	n := 0
	for _, d := range world.Schedule.Dinners {
		if d.Season.ID == last.ID {
			n++
		}
	}
	return (last.RotationOffset + n) % active
}

// adminRedirect sends the browser back to the part of the admin view the form
// was posted from, with a word about what happened.
//
// The address is built here rather than taken from the form. It used to be a
// hidden "back" field holding a whole URL, which went wrong twice over: the
// tab was not in it, so every save landed on the first tab, and the "sparat"
// query was appended after the "#lag" fragment, where a browser reads it as
// part of the fragment and never sends it — so the confirmation never showed
// either. A form only needs to say which tab it belongs to.
func (s *Server) adminRedirect(w http.ResponseWriter, r *http.Request, saved string) {
	tab := adminTab(r.FormValue("flik"))
	q := url.Values{}
	q.Set("flik", tab)
	// The schedule is shown one season at a time, so a save there has to come
	// back to the season it was made in rather than to whichever one is
	// current.
	if raw := strings.TrimSpace(r.FormValue("sasong")); raw != "" {
		if _, err := strconv.ParseInt(raw, 10, 64); err == nil {
			q.Set("sasong", raw)
		}
	}
	q.Set("sparat", saved)
	http.Redirect(w, r, "/admin?"+q.Encode()+"#"+tab, http.StatusSeeOther)
}

func (s *Server) handleAdminTeam(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)

	if r.FormValue("action") == "delete" {
		if err := s.store.DeleteTeam(ctx, id); err != nil {
			s.fail(w, r, "delete team", err)
			return
		}
		s.log.Info("team deleted", "id", id)
		s.adminRedirect(w, r, "lag-borttaget")
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.errorPage(w, r, http.StatusUnprocessableEntity,
			"error.team.name", "error.team.name.detail")
		return
	}
	typed := strings.TrimSpace(r.FormValue("leader_username"))
	// The same lookup the household's own field uses, and the same sentences
	// back: a name that means one person is that person, a name that means
	// several says which, and a name that means nobody is refused.
	leader, problem := s.resolveLeader(ctx, v.Lang, typed)
	if problem != "" {
		s.renderError(w, r, http.StatusUnprocessableEntity,
			i18n.T(v.Lang, "error.team.leader"), problem)
		return
	}
	// The name is never typed: it comes from the account, spelled the way its
	// owner spells it. Nothing to keep in step, and nothing to get wrong.
	leaderName := ""
	if leader.ID != "" {
		leaderName = leader.DisplayName()
	}
	if _, err := s.store.SaveTeam(ctx, store.Team{
		ID:             id,
		Name:           name,
		LeaderName:     leaderName,
		LeaderUsername: leader.Username,
		Active:         r.FormValue("active") != "",
	}); err != nil {
		s.fail(w, r, "save team", err)
		return
	}
	s.log.Info("team saved", "name", name, "leader", leader.Username)
	s.adminRedirect(w, r, "lag")
}

// handleAdminTeams saves every cooking team in one go, which is how the teams
// tab is actually used: setting a season up means naming four or five teams and
// their leaders, and saving each one on its own meant a page load per team and
// a scroll back to where you were.
//
// The rows post their fields keyed by team id — name_7, leader_7, active_7 —
// so one form can carry them all without the values of one row being mistaken
// for another's.
//
// Every leader is looked up before anything is written. A team whose leader is
// a typo would otherwise be saved without one while its neighbours went
// through, leaving the administrator to work out which of five rows was
// refused; this way nothing changes until all of them can.
func (s *Server) handleAdminTeams(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}

	// The delete buttons sit inside this form, so they arrive here.
	if raw := r.FormValue("delete"); raw != "" {
		id := parseID(raw)
		if err := s.store.DeleteTeam(ctx, id); err != nil {
			s.fail(w, r, "delete team", err)
			return
		}
		s.log.Info("team deleted", "id", id)
		s.adminRedirect(w, r, "lag-borttaget")
		return
	}

	current, err := s.store.Teams(ctx)
	if err != nil {
		s.fail(w, r, "read teams", err)
		return
	}
	known := make(map[int64]store.Team, len(current))
	for _, t := range current {
		known[t.ID] = t
	}

	var (
		pending  []store.Team
		problems []string
	)
	for _, raw := range r.Form["id"] {
		id := parseID(raw)
		was, ok := known[id]
		if !ok {
			// A team deleted in another tab while this page was open. Saving a
			// row for it would resurrect it under a new id, so skip it.
			s.log.Warn("skipping a team that no longer exists", "id", id)
			continue
		}
		name := strings.TrimSpace(r.FormValue("name_" + raw))
		if name == "" {
			s.errorPage(w, r, http.StatusUnprocessableEntity,
				"error.team.name", "error.team.name.detail")
			return
		}
		leader, problem := s.resolveLeader(ctx, v.Lang, r.FormValue("leader_"+raw))
		if problem != "" {
			// Name the row: with five of them on screen, "several people are
			// called Anna" is only useful once you know which team it is about.
			problems = append(problems, was.Name+": "+problem)
			continue
		}
		leaderName := ""
		if leader.ID != "" {
			leaderName = leader.DisplayName()
		}
		pending = append(pending, store.Team{
			ID:             id,
			Name:           name,
			LeaderName:     leaderName,
			LeaderUsername: leader.Username,
			Active:         r.FormValue("active_"+raw) != "",
		})
	}

	if len(problems) > 0 {
		s.renderError(w, r, http.StatusUnprocessableEntity,
			i18n.T(v.Lang, "error.team.leader"), strings.Join(problems, " "))
		return
	}

	for _, t := range pending {
		if _, err := s.store.SaveTeam(ctx, t); err != nil {
			s.fail(w, r, "save team", err)
			return
		}
	}
	s.log.Info("teams saved", "count", len(pending))
	s.adminRedirect(w, r, "lag")
}

func (s *Server) handleAdminSeason(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)

	if r.FormValue("action") == "delete" {
		if err := s.store.DeleteSeason(ctx, id); err != nil {
			s.fail(w, r, "delete season", err)
			return
		}
		s.log.Info("season deleted", "id", id)
		s.adminRedirect(w, r, "sasong-borttagen")
		return
	}

	loc := s.cfg.Location()
	start, err := config.ParseDate(formDate(r, "start"), loc)
	if err != nil {
		s.errorPage(w, r, http.StatusUnprocessableEntity,
			"error.startdate", "error.startdate.detail")
		return
	}
	end, err := config.ParseDate(formDate(r, "end"), loc)
	if err != nil {
		s.errorPage(w, r, http.StatusUnprocessableEntity,
			"error.enddate", "error.enddate.detail")
		return
	}
	if end.Before(start) {
		s.errorPage(w, r, http.StatusUnprocessableEntity,
			"error.season.order", "error.dates.check")
		return
	}
	var weekdays []time.Weekday
	for _, raw := range r.Form["weekdays"] {
		if wd, err := config.ParseWeekday(raw); err == nil {
			weekdays = append(weekdays, wd)
		}
	}
	if len(weekdays) == 0 {
		s.errorPage(w, r, http.StatusUnprocessableEntity,
			"error.season.weekdays", "error.season.weekdays.detail")
		return
	}
	// Two seasons covering the same day would disagree about whether it is a
	// dinner and who cooks it, so the second one is refused rather than
	// silently ignored when the schedule is built.
	existing, err := s.store.Seasons(ctx)
	if err != nil {
		s.fail(w, r, "read seasons", err)
		return
	}
	proposed := store.Season{
		ID:    id,
		Start: start.Format("2006-01-02"),
		End:   end.Format("2006-01-02"),
	}
	for _, other := range existing {
		if other.ID == id || !proposed.Overlaps(other) {
			continue
		}
		s.renderError(w, r, http.StatusConflict,
			i18n.T(v.Lang, "error.season.overlap"),
			i18n.T(v.Lang, "error.season.overlap.detail", other.Name, other.Start, other.End))
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "Säsong " + start.Format("2006")
	}
	offset, _ := strconv.Atoi(r.FormValue("rotation_offset"))
	_, err = s.store.SaveSeason(ctx, store.Season{
		ID:             id,
		Name:           name,
		Start:          start.Format("2006-01-02"),
		End:            end.Format("2006-01-02"),
		Weekdays:       weekdays,
		RotationOffset: offset,
	})
	if err != nil {
		s.fail(w, r, "save season", err)
		return
	}
	s.log.Info("season saved", "name", name, "start", start.Format("2006-01-02"))
	s.adminRedirect(w, r, "sasong")
}

// handleAdminOrder rewrites the rotation order. It takes either a whole
// sequence — what the drag-and-drop list posts — or a single team to nudge one
// step, which is how the arrows work when there is no JavaScript.
func (s *Server) handleAdminOrder(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}

	current, err := s.store.Teams(ctx)
	if err != nil {
		s.fail(w, r, "read teams", err)
		return
	}
	order := make([]int64, 0, len(current))
	for _, t := range current {
		order = append(order, t.ID)
	}

	switch {
	case r.FormValue("up") != "":
		order = nudge(order, parseID(r.FormValue("up")), -1)
	case r.FormValue("down") != "":
		order = nudge(order, parseID(r.FormValue("down")), 1)
	default:
		order = parseIDs(r.FormValue("order"))
	}

	if err := s.store.ReorderTeams(ctx, order); err != nil {
		s.fail(w, r, "reorder teams", err)
		return
	}
	s.log.Info("rotation order changed")
	s.adminRedirect(w, r, "ordning")
}

// nudge moves one id a step towards the front or the back, and leaves the
// order alone if it is already at that end.
func nudge(order []int64, id int64, step int) []int64 {
	for i, at := range order {
		if at != id {
			continue
		}
		j := i + step
		if j < 0 || j >= len(order) {
			return order
		}
		order[i], order[j] = order[j], order[i]
		return order
	}
	return order
}

func parseID(raw string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return n
}

func parseIDs(raw string) []int64 {
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		if n := parseID(part); n != 0 {
			out = append(out, n)
		}
	}
	return out
}

func (s *Server) handleAdminBreak(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)

	if r.FormValue("action") == "delete" {
		if err := s.store.DeleteBreak(ctx, id); err != nil {
			s.fail(w, r, "delete break", err)
			return
		}
		s.log.Info("break deleted", "id", id)
		s.adminRedirect(w, r, "uppehall-borttaget")
		return
	}

	loc := s.cfg.Location()
	startRaw := formDate(r, "start")
	start, err := config.ParseDate(startRaw, loc)
	if err != nil {
		s.errorPage(w, r, http.StatusUnprocessableEntity,
			"error.startdate", "error.startdate.break")
		return
	}
	// A single day off is a break from and to the same date, so an empty end
	// date means "just that day" rather than being an error.
	endRaw := formDate(r, "end")
	if endRaw == "" {
		endRaw = startRaw
	}
	end, err := config.ParseDate(endRaw, loc)
	if err != nil {
		s.errorPage(w, r, http.StatusUnprocessableEntity,
			"error.enddate", "error.enddate.break")
		return
	}
	if end.Before(start) {
		s.errorPage(w, r, http.StatusUnprocessableEntity,
			"error.break.order", "error.dates.check")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "Uppehåll"
	}
	if _, err := s.store.SaveBreak(ctx, store.Break{
		ID:    id,
		Name:  name,
		Start: start.Format("2006-01-02"),
		End:   end.Format("2006-01-02"),
	}); err != nil {
		s.fail(w, r, "save break", err)
		return
	}
	s.log.Info("break saved", "name", name,
		"start", start.Format("2006-01-02"), "end", end.Format("2006-01-02"))
	s.adminRedirect(w, r, "uppehall")
}

func (s *Server) handleAdminSchedule(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}
	date := strings.TrimSpace(r.FormValue("date"))
	if _, err := config.ParseDate(date, s.cfg.Location()); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.date", "error.form.detail")
		return
	}
	var team sql.NullInt64
	if raw := r.FormValue("team_id"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			team = sql.NullInt64{Int64: n, Valid: true}
		}
	}
	err := s.store.SaveOverride(ctx, store.Override{
		Date:      date,
		Cancelled: r.FormValue("cancelled") != "",
		TeamID:    team,
		Note:      r.FormValue("note"),
		UpdatedAt: s.now(),
	})
	if err != nil {
		s.fail(w, r, "save schedule exception", err)
		return
	}
	s.log.Info("schedule exception saved", "date", date)
	s.adminRedirect(w, r, "schema")
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}
	wd, err := config.ParseWeekday(r.FormValue("deadline_weekday"))
	if err != nil {
		s.errorPage(w, r, http.StatusUnprocessableEntity,
			"error.deadline.weekday", "error.form.detail")
		return
	}
	minutes, err := config.ParseClock(r.FormValue("deadline_time"))
	if err != nil {
		s.errorPage(w, r, http.StatusUnprocessableEntity,
			"error.deadline.time", "error.deadline.time.detail")
		return
	}
	weeks, err := strconv.Atoi(strings.TrimSpace(r.FormValue("deadline_weeks_before")))
	if err != nil || weeks < 0 || weeks > 8 {
		s.errorPage(w, r, http.StatusUnprocessableEntity,
			"error.deadline.weeks", "error.deadline.weeks.detail")
		return
	}
	err = s.store.SaveSettings(ctx, store.Settings{
		DeadlineWeekday:     wd,
		DeadlineMinutes:     minutes,
		DeadlineWeeksBefore: weeks,
		GuestOpen:           r.FormValue("guest_open") != "",
	})
	if err != nil {
		s.fail(w, r, "save settings", err)
		return
	}
	s.log.Info("settings saved", "deadline", wd.String()+" "+config.FormatClock(minutes), "weeks_before", weeks)
	s.adminRedirect(w, r, "installningar")
}

// handleAdminSend sends one evening's list on demand: a team leader who lost
// the message, or a list that is worth sending again after a late change.
func (s *Server) handleAdminSend(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	d, ok := world.Schedule.Find(strings.TrimSpace(r.FormValue("date")))
	if !ok {
		s.errorPage(w, r, http.StatusNotFound,
			"error.nodinner", "error.nodinner.check")
		return
	}
	// Sending again means forgetting that we sent before, so the log keeps
	// showing when the leader last heard from us.
	if err := s.store.ClearNotified(ctx, d.Key, notifyKind); err != nil {
		s.fail(w, r, "clear the notification log", err)
		return
	}
	if err := s.sendList(ctx, d); err != nil {
		s.log.Error("send list on demand", "date", d.Key, "err", err)
		s.adminRedirect(w, r, "utskick-misslyckades")
		return
	}
	s.adminRedirect(w, r, "utskick")
}

func (s *Server) handleAdminDeleteRegistration(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}
	if err := s.store.DeleteRegistration(ctx, r.FormValue("id")); err != nil {
		s.fail(w, r, "delete registration", err)
		return
	}
	s.log.Info("registration deleted by admin", "id", r.FormValue("id"))
	s.adminRedirect(w, r, "anmalan-borttagen")
}

// handleAdminCSV exports the registrations of a season, for the treasurer who
// bills the households and for anyone who wants a spreadsheet after all.
func (s *Server) handleAdminCSV(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	from := r.URL.Query().Get("fran")
	to := r.URL.Query().Get("till")
	if from == "" || to == "" {
		if se := currentSeason(world, v.Now); se != nil {
			from, to = se.Start, se.End
		} else {
			from, to = "0000-01-01", "9999-12-31"
		}
	}

	var dinners []dinner.Dinner
	for _, d := range world.Schedule.Dinners {
		if d.Key >= from && d.Key <= to {
			dinners = append(dinners, d)
		}
	}
	summaries, err := s.summarize(ctx, dinners)
	if err != nil {
		s.fail(w, r, "summarize dinners", err)
		return
	}

	// The export is read in a spreadsheet, so it follows the reader's language
	// like every other page rather than the deployment default.
	lang := v.Lang
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="middagar-%s-%s.csv"`, from, to))
	cw := csv.NewWriter(w)
	defer cw.Flush()
	cw.Write([]string{"datum", "veckodag", "matlag", "instald", "namn",
		"lagenhet", "mattermost", "gast", "vard", "staende", "vuxna", "barn",
		"kosthallning", "specialkost"})
	for _, d := range dinners {
		team := ""
		if d.Team != nil {
			team = d.Team.Name
		}
		cancelled := ""
		if d.Cancelled {
			cancelled = "ja"
		}
		for _, a := range summaries[d.Key].Attendees {
			cw.Write([]string{
				d.Key, i18n.WeekdayShort(lang, d.Date), team, cancelled,
				a.Name, a.Apartment, a.Member,
				yesNo(a.Guest), a.Host, yesNo(a.Standing),
				strconv.Itoa(a.Adults), strconv.Itoa(a.Children),
				string(a.Diet), a.Note,
			})
		}
	}
}

func yesNo(b bool) string {
	if b {
		return "ja"
	}
	return ""
}
