package web

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/auth"
	"github.com/O5ten/dinners/internal/config"
	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/store"
)

// scheduleRow is one evening in the admin schedule, with everything the
// administrator needs to judge it at a glance.
type scheduleRow struct {
	Dinner  dinner.Dinner
	Summary dinner.Summary
	SentAt  time.Time
	Sent    bool
	// Unmanned marks an evening whose deadline passed with no cooking team to
	// tell. Nothing was mailed; the row says so rather than claiming it was.
	Unmanned bool
	Open     bool
	Over     bool
	ListURL  string
}

// adminTabs are the admin view's pages, in the order the tab bar shows them.
// Splitting them up keeps each page to one job — and keeps the server from
// building a season's whole schedule when all you wanted was to fix a typo in
// a team name.
var adminTabs = []struct{ ID, Name string }{
	{"schema", "Schema"},
	{"lag", "Matlag"},
	{"sasonger", "Säsonger"},
	{"uppehall", "Uppehåll"},
	{"installningar", "Inställningar"},
	{"staende", "Stående anmälningar"},
}

func adminTab(raw string) string {
	for _, t := range adminTabs {
		if t.ID == raw {
			return t.ID
		}
	}
	return adminTabs[0].ID
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
			s.fail(w, r, "read mail log", err)
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

	v.Title = "Administration"
	v.Data = map[string]any{
		"Tab":              tab,
		"Tabs":             adminTabs,
		"Breaks":           world.Breaks,
		"Settings":         world.Settings,
		"Teams":            world.Teams,
		"Seasons":          world.Seasons,
		"Season":           shown,
		"Rows":             rows,
		"Standing":         standing,
		"Weekdays":         allWeekdays(),
		"GuestURL":         s.rt.BaseURL + "/gast",
		"Saved":            r.URL.Query().Get("sparat"),
		"MailOn":           s.mailer.Enabled(),
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

func (s *Server) adminRedirect(w http.ResponseWriter, r *http.Request, saved string) {
	back := r.FormValue("back")
	if back == "" {
		back = "/admin"
	}
	// Never redirect anywhere but back into the admin view: `back` comes from
	// the form, and a form field is not a safe place to learn a URL from.
	if !strings.HasPrefix(back, "/admin") {
		back = "/admin"
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"sparat="+saved, http.StatusSeeOther)
}

func (s *Server) handleAdminTeam(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Formuläret kunde inte läsas", "Försök igen.")
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
	email := auth.NormalizeEmail(r.FormValue("leader_email"))
	if name == "" {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Laget behöver ett namn",
			"Skriv till exempel \"Lag 1\".")
		return
	}
	if email != "" && !auth.ValidEmail(email) {
		s.renderError(w, r, http.StatusUnprocessableEntity, "E-postadressen ser inte riktig ut",
			"Utan en adress som fungerar får lagledaren ingen matlista.")
		return
	}
	_, err := s.store.SaveTeam(ctx, store.Team{
		ID:          id,
		Name:        name,
		LeaderName:  strings.TrimSpace(r.FormValue("leader_name")),
		LeaderEmail: email,
		Active:      r.FormValue("active") != "",
	})
	if err != nil {
		s.fail(w, r, "save team", err)
		return
	}
	s.log.Info("team saved", "name", name)
	s.adminRedirect(w, r, "lag")
}

func (s *Server) handleAdminSeason(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Formuläret kunde inte läsas", "Försök igen.")
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
	start, err := config.ParseDate(r.FormValue("start"), loc)
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Startdatumet går inte att läsa",
			"Skriv det som 2026-08-25.")
		return
	}
	end, err := config.ParseDate(r.FormValue("end"), loc)
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Slutdatumet går inte att läsa",
			"Skriv det som 2026-12-17.")
		return
	}
	if end.Before(start) {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Säsongen slutar innan den börjar",
			"Kontrollera datumen.")
		return
	}
	var weekdays []time.Weekday
	for _, raw := range r.Form["weekdays"] {
		if wd, err := config.ParseWeekday(raw); err == nil {
			weekdays = append(weekdays, wd)
		}
	}
	if len(weekdays) == 0 {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Välj minst en middagskväll",
			"En säsong utan kvällar ger inga middagar att anmäla sig till.")
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
			"Säsongerna överlappar varandra",
			fmt.Sprintf("Perioden krockar med %q (%s – %s). Två säsonger kan inte gälla samma dag — flytta datumen eller ändra den andra säsongen först.",
				other.Name, other.Start, other.End))
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
		s.renderError(w, r, http.StatusBadRequest, "Formuläret kunde inte läsas", "Försök igen.")
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
		s.renderError(w, r, http.StatusBadRequest, "Formuläret kunde inte läsas", "Försök igen.")
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
	start, err := config.ParseDate(r.FormValue("start"), loc)
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Startdatumet går inte att läsa",
			"Skriv det som 2026-10-26.")
		return
	}
	// A single day off is a break from and to the same date, so an empty end
	// date means "just that day" rather than being an error.
	endRaw := strings.TrimSpace(r.FormValue("end"))
	if endRaw == "" {
		endRaw = r.FormValue("start")
	}
	end, err := config.ParseDate(endRaw, loc)
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Slutdatumet går inte att läsa",
			"Skriv det som 2026-11-01, eller lämna det tomt för en enda dag.")
		return
	}
	if end.Before(start) {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Uppehållet slutar innan det börjar",
			"Kontrollera datumen.")
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
		s.renderError(w, r, http.StatusBadRequest, "Formuläret kunde inte läsas", "Försök igen.")
		return
	}
	date := strings.TrimSpace(r.FormValue("date"))
	if _, err := config.ParseDate(date, s.cfg.Location()); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Okänt datum", "Försök igen.")
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
		s.renderError(w, r, http.StatusBadRequest, "Formuläret kunde inte läsas", "Försök igen.")
		return
	}
	wd, err := config.ParseWeekday(r.FormValue("deadline_weekday"))
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Okänd veckodag för anmälningsstopp", "Försök igen.")
		return
	}
	minutes, err := config.ParseClock(r.FormValue("deadline_time"))
	if err != nil {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Klockslaget går inte att läsa",
			"Skriv det som 23:59.")
		return
	}
	weeks, err := strconv.Atoi(strings.TrimSpace(r.FormValue("deadline_weeks_before")))
	if err != nil || weeks < 0 || weeks > 8 {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Antalet veckor går inte att läsa",
			"Skriv hur många hela veckor före middagsveckan anmälan ska stänga, till exempel 1.")
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

// handleAdminSend mails one evening's list on demand: a team leader who lost
// the mail, or a list that is worth sending again after a late change.
func (s *Server) handleAdminSend(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Formuläret kunde inte läsas", "Försök igen.")
		return
	}
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	d, ok := world.Schedule.Find(strings.TrimSpace(r.FormValue("date")))
	if !ok {
		s.renderError(w, r, http.StatusNotFound, "Ingen middag den dagen", "Kontrollera datumet.")
		return
	}
	// Sending again means forgetting that we sent before, so the log keeps
	// showing when the leader last heard from us.
	if err := s.store.ClearNotified(ctx, d.Key, notifyKind); err != nil {
		s.fail(w, r, "clear mail log", err)
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
		s.renderError(w, r, http.StatusBadRequest, "Formuläret kunde inte läsas", "Försök igen.")
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

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="middagar-%s-%s.csv"`, from, to))
	cw := csv.NewWriter(w)
	defer cw.Flush()
	cw.Write([]string{"datum", "veckodag", "matlag", "instald", "namn",
		"lagenhet", "epost", "gast", "vard", "staende", "vuxna", "barn",
		"veganer", "vegetarianer", "allatare", "specialkost"})
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
				d.Key, WeekdayShort(d.Date), team, cancelled,
				a.Name, a.Apartment, a.Email,
				yesNo(a.Guest), a.Host, yesNo(a.Standing),
				strconv.Itoa(a.Adults), strconv.Itoa(a.Children),
				strconv.Itoa(a.Vegans), strconv.Itoa(a.Vegetarians),
				strconv.Itoa(a.Omnivores()), a.Note,
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
