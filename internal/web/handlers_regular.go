package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/auth"
	"github.com/O5ten/dinners/internal/config"
	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/store"
)

// A regular — a stamgäst — is a friend of the house who eats here every week
// without being one of the households: a grown-up child who comes home for
// Thursday dinner, a neighbour, a parent in the flat next door. They are not in
// the house's Mattermost and may well never be, so the ordinary standing
// registration is closed to them: it hangs on an account.
//
// What they get instead is the guest page's bargain, stretched from one evening
// to every week. They ask on the open page, naming who in the house they eat
// with; the request waits for the cooking teams' administrator to agree to it;
// and their own link is the whole relationship afterwards, the way it is for a
// one-off visitor. Nothing is sent to them, because there is nothing to send it
// to.
//
// The approval is the point. A standing registration writes somebody onto every
// list from now on, and a household that does that has at least got past the
// house password and named an account the house can find. A regular has done
// neither, so somebody in the house says yes first, and can say no again later.

// regularThrottle bounds how many requests one address may make. Approval
// already means nothing gets onto a list unasked; this only keeps the queue the
// administrator reads from being filled with nonsense.
var regularThrottle = newThrottle(10)

// regularForm is what a friend of the house fills in to ask to be counted in
// every week.
type regularForm struct {
	Name string
	// Host is somebody in the house who knows them, if there is one to name.
	// It is a help and not a gate: plenty of regulars are simply friends and
	// family of the house rather than one household's visitor, and asking them
	// to pick a member would only invite a made-up name. What keeps the lists
	// honest is the approval, not this field.
	Host     string
	Weekdays []time.Weekday
	Adults   int
	Children int
	Diet     store.Diet
	Note     string
}

// regularEvening is one upcoming evening a regular is counted in on, and what
// they have said about it if anything.
type regularEvening struct {
	Dinner dinner.Dinner
	Form   regForm
	// Answered marks an evening they have given their own answer for, rather
	// than being carried along by the standing registration.
	Answered bool
	// Skipping marks an answer for nobody: they are not coming that evening.
	Skipping bool
}

func (s *Server) regularView(r *http.Request) *view {
	v := s.newView(r, s.guard.Role(r))
	v.Title = i18n.T(v.Lang, "regular.title")
	return v
}

// handleRegularForm is the open page a friend of the house asks on.
func (s *Server) handleRegularForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	v := s.regularView(r)
	v.GuestOpen = world.Settings.GuestOpen
	if !world.Settings.GuestOpen {
		s.errorPage(w, r, http.StatusNotFound,
			"error.guest.closed", "error.guest.closed.detail")
		return
	}
	v.Data = map[string]any{
		"Weekdays": dinnerWeekdays(world, s.cfg),
		"Form":     regularForm{Adults: 1, Diet: store.DietOmnivore},
	}
	s.render(w, r, http.StatusOK, "regular.html", v)
}

// handleRegularSave takes the request. One row is written per evening asked
// for, all of them sharing the link that is now the guest's: the request is one
// thing to approve and one thing to withdraw.
func (s *Server) handleRegularSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	v := s.regularView(r)
	v.GuestOpen = world.Settings.GuestOpen
	if !world.Settings.GuestOpen {
		s.errorPage(w, r, http.StatusNotFound,
			"error.guest.closed", "error.guest.closed.detail")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}

	offered := dinnerWeekdays(world, s.cfg)
	counts := readForm(r)
	form := regularForm{
		Name:     strings.TrimSpace(r.FormValue("name")),
		Host:     strings.TrimSpace(r.FormValue("host")),
		Weekdays: pickedWeekdays(r, offered),
		Adults:   counts.Adults, Children: counts.Children,
		Diet: counts.Diet, Note: counts.Note,
	}

	reject := func(problem string, status int) {
		v.Data = map[string]any{"Weekdays": offered, "Form": form, "Error": problem}
		s.render(w, r, status, "regular.html", v)
	}

	ip := s.clientIP(r)
	now := s.now()
	if !regularThrottle.allow(ip, now) {
		reject(i18n.T(v.Lang, "guest.throttled"), http.StatusTooManyRequests)
		return
	}
	switch {
	case form.Name == "":
		reject(i18n.T(v.Lang, "guest.needname"), http.StatusUnprocessableEntity)
		return
	case len(form.Weekdays) == 0:
		reject(i18n.T(v.Lang, "regular.needevening"), http.StatusUnprocessableEntity)
		return
	case counts.Adults+counts.Children <= 0:
		reject(i18n.T(v.Lang, "guest.atleastone"), http.StatusUnprocessableEntity)
		return
	}
	if problem := validateParty(v.Lang, counts); problem != "" {
		reject(problem, http.StatusUnprocessableEntity)
		return
	}

	regularThrottle.fail(ip, now)
	token := auth.Token()
	for _, wd := range form.Weekdays {
		err := s.store.SaveStanding(ctx, store.Standing{
			ID:      auth.ID(),
			Kind:    store.KindGuest,
			Token:   token,
			Weekday: wd,
			Name:    form.Name,
			Host:    form.Host,
			Adults:  form.Adults, Children: form.Children,
			Diet: form.Diet, Note: form.Note,
			// Pending, and dated: the timestamp that decides which evenings it
			// is in force for is set when it is approved, not now.
			Status:    store.StatusPending,
			CreatedAt: now, UpdatedAt: now,
			CreatedIP: ip,
		})
		if err != nil {
			s.fail(w, r, "save the request to be a regular", err)
			return
		}
	}
	s.log.Info("regular guest asked to be counted in", "name", form.Name,
		"host", form.Host, "evenings", len(form.Weekdays),
		"people", form.Adults+form.Children)
	http.Redirect(w, r, "/stamgast/"+token+"?sparat=1", http.StatusSeeOther)
}

// pickedWeekdays reads the evenings ticked on the form, keeping only the ones
// the house actually cooks and the order they are offered in.
func pickedWeekdays(r *http.Request, offered []time.Weekday) []time.Weekday {
	picked := map[time.Weekday]bool{}
	for _, raw := range r.Form["weekdays"] {
		if wd, err := config.ParseWeekday(raw); err == nil {
			picked[wd] = true
		}
	}
	var out []time.Weekday
	for _, wd := range offered {
		if picked[wd] {
			out = append(out, wd)
		}
	}
	return out
}

// regular is one request or one approved regular, read from the rows that share
// a link. They are saved and changed together, so the first row speaks for all
// of them and the weekdays are what differ.
type regular struct {
	Rows     []store.Standing
	Weekdays []time.Weekday
}

func (s *Server) regularByToken(ctx context.Context, token string) (regular, error) {
	rows, err := s.store.StandingByToken(ctx, token)
	if err != nil {
		return regular{}, err
	}
	if len(rows) == 0 {
		return regular{}, store.ErrNotFound
	}
	out := regular{Rows: rows}
	for _, st := range rows {
		out.Weekdays = append(out.Weekdays, st.Weekday)
	}
	return out, nil
}

// first is the row that speaks for the request: everything but the weekday is
// the same across all of them.
func (g regular) first() store.Standing { return g.Rows[0] }

func (g regular) covers(wd time.Weekday) bool {
	for _, at := range g.Weekdays {
		if at == wd {
			return true
		}
	}
	return false
}

// handleRegularPage is a regular's own page: where the request stands, what it
// says, and — once it is approved — the evenings it is about to count them in
// on, each of which they can change or sit out.
func (s *Server) handleRegularPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := r.PathValue("token")
	g, err := s.regularByToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		s.errorPage(w, r, http.StatusNotFound,
			"error.reg.notfound", "error.reg.notfound.detail")
		return
	}
	if err != nil {
		s.fail(w, r, "read the regular's standing registration", err)
		return
	}
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	v := s.regularView(r)
	v.GuestOpen = world.Settings.GuestOpen
	evenings, err := s.regularEvenings(ctx, world, g, v.Now)
	if err != nil {
		s.fail(w, r, "read the regular's evenings", err)
		return
	}
	s.renderRegular(w, r, v, g, evenings, g.form(), "", http.StatusOK)
}

// form is what the request says now, for the page to offer back for editing.
func (g regular) form() regularForm {
	st := g.first()
	return regularForm{
		Name: st.Name, Host: st.Host, Weekdays: g.Weekdays,
		Adults: st.Adults, Children: st.Children,
		Diet: st.Diet, Note: st.Note,
	}
}

func (s *Server) renderRegular(w http.ResponseWriter, r *http.Request, v *view,
	g regular, evenings []regularEvening, form regularForm, problem string, status int) {

	st := g.first()
	v.Title = i18n.T(v.Lang, "regular.yours")
	v.Data = map[string]any{
		"Token":    st.Token,
		"Pending":  st.Pending(),
		"Weekdays": g.Weekdays,
		"Evenings": evenings,
		"AskedAt":  st.CreatedAt.In(v.Loc),
		"Saved":    r.URL.Query().Get("sparat") != "",
		"Error":    problem,
		"Form":     form,
	}
	s.render(w, r, status, "regular.html", v)
}

// regularEvenings are the evenings still taking answers that this regular is
// counted in on, with what they have said about each. A request nobody has
// agreed to yet is about no evening at all, so it has none.
func (s *Server) regularEvenings(ctx context.Context, world *world, g regular, now time.Time) ([]regularEvening, error) {
	st := g.first()
	if !st.Counts() {
		return nil, nil
	}
	var out []regularEvening
	for _, d := range world.Schedule.Upcoming(now, 0) {
		if !d.Open(now) || !g.covers(d.Weekday()) {
			continue
		}
		ev := regularEvening{
			Dinner: d,
			Form: regForm{
				Adults: st.Adults, Children: st.Children,
				Diet: st.Diet, Note: st.Note, FromStanding: true,
			},
		}
		switch reg, err := s.store.StandingException(ctx, d.Key, st.Token); {
		case err == nil:
			ev.Answered = true
			ev.Skipping = !reg.Attending()
			ev.Form = regForm{
				Adults: reg.Adults, Children: reg.Children,
				Diet: reg.Diet, Note: reg.Note, Answered: true,
			}
		case errors.Is(err, store.ErrNotFound):
		default:
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// handleRegularUpdate changes or withdraws the whole request. The numbers, the
// meal and the note are one answer for every evening it covers, the way they
// were when it was asked for; which evenings those are is settled at that point
// and changed by withdrawing and asking again.
func (s *Server) handleRegularUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, err := s.regularByToken(ctx, r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		s.errorPage(w, r, http.StatusNotFound,
			"error.reg.notfound", "error.reg.notfound.detail")
		return
	}
	if err != nil {
		s.fail(w, r, "read the regular's standing registration", err)
		return
	}
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
	v := s.regularView(r)
	v.GuestOpen = world.Settings.GuestOpen
	st := g.first()
	ip := s.clientIP(r)

	if r.FormValue("action") == "delete" {
		if err := s.revokeRegular(ctx, ip, g); err != nil {
			s.fail(w, r, "withdraw the regular's standing registration", err)
			return
		}
		s.log.Info("regular guest withdrew", "name", st.Name)
		v.Title = i18n.T(v.Lang, "regular.removed")
		v.Data = map[string]any{"Removed": true}
		s.render(w, r, http.StatusOK, "regular.html", v)
		return
	}

	counts := readForm(r)
	form := regularForm{
		Name:     strings.TrimSpace(r.FormValue("name")),
		Host:     strings.TrimSpace(r.FormValue("host")),
		Weekdays: g.Weekdays,
		Adults:   counts.Adults, Children: counts.Children,
		Diet: counts.Diet, Note: counts.Note,
	}
	// The page comes back with the submitted values rather than the stored
	// ones, so nothing typed is lost to a complaint about one field.
	reject := func(problem string) {
		evenings, err := s.regularEvenings(ctx, world, g, v.Now)
		if err != nil {
			s.fail(w, r, "read the regular's evenings", err)
			return
		}
		s.renderRegular(w, r, v, g, evenings, form, problem, http.StatusUnprocessableEntity)
	}
	switch {
	case form.Name == "":
		reject(i18n.T(v.Lang, "guest.needname"))
		return
	case counts.Adults+counts.Children <= 0:
		reject(i18n.T(v.Lang, "regular.atleastone.or"))
		return
	}
	if problem := validateParty(v.Lang, counts); problem != "" {
		reject(problem)
		return
	}

	// The evenings already sent to their cooking teams keep the numbers they
	// were sent with, exactly as they do when a household edits its own
	// standing registration.
	now := s.now()
	for _, was := range g.Rows {
		if err := s.freezeClosedEvenings(ctx, ip, was); err != nil {
			s.fail(w, r, "keep the closed evenings as they were sent", err)
			return
		}
		err := s.store.SaveStanding(ctx, store.Standing{
			ID:      auth.ID(),
			Kind:    store.KindGuest,
			Token:   was.Token,
			Weekday: was.Weekday,
			Name:    form.Name,
			Host:    form.Host,
			Adults:  form.Adults, Children: form.Children,
			Diet: form.Diet, Note: form.Note,
			// A change is not a new request: an approved regular stays
			// approved, and one still waiting stays in the queue.
			Status:    was.Status,
			CreatedAt: was.CreatedAt,
			UpdatedAt: now,
			CreatedIP: was.CreatedIP,
		})
		if err != nil {
			s.fail(w, r, "save the regular's standing registration", err)
			return
		}
	}
	s.log.Info("regular guest changed their standing registration",
		"name", form.Name, "people", form.Adults+form.Children)
	http.Redirect(w, r, "/stamgast/"+st.Token+"?sparat=1", http.StatusSeeOther)
}

// handleRegularEvening is one evening's exception: different numbers this once,
// or nobody at all. It is the same thing a household does from the evening's
// own page, which a regular cannot open.
func (s *Server) handleRegularEvening(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, err := s.regularByToken(ctx, r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		s.errorPage(w, r, http.StatusNotFound,
			"error.reg.notfound", "error.reg.notfound.detail")
		return
	}
	if err != nil {
		s.fail(w, r, "read the regular's standing registration", err)
		return
	}
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
	v := s.regularView(r)
	v.GuestOpen = world.Settings.GuestOpen
	st := g.first()
	if !st.Counts() {
		s.errorPage(w, r, http.StatusConflict,
			"regular.notyet", "regular.notyet.detail")
		return
	}
	d, ok := world.Schedule.Find(strings.TrimSpace(r.FormValue("date")))
	if !ok || !g.covers(d.Weekday()) {
		s.errorPage(w, r, http.StatusNotFound,
			"error.nodinner", "error.nodinner.check")
		return
	}
	if !d.Open(v.Now) {
		s.errorPage(w, r, http.StatusConflict,
			"error.reg.closed", "error.reg.closed.detail")
		return
	}

	// Back to the standing registration: the evening has nothing of its own to
	// say any more.
	if r.FormValue("action") == "clear" {
		switch reg, err := s.store.StandingException(ctx, d.Key, st.Token); {
		case err == nil:
			if err := s.store.DeleteRegistration(ctx, reg.ID); err != nil {
				s.fail(w, r, "clear the regular's answer for one evening", err)
				return
			}
		case errors.Is(err, store.ErrNotFound):
		default:
			s.fail(w, r, "read the regular's answer for one evening", err)
			return
		}
		http.Redirect(w, r, "/stamgast/"+st.Token+"?sparat=1", http.StatusSeeOther)
		return
	}

	counts := readForm(r)
	// "I am not coming this evening" is a registration for nobody, which is
	// what beats a standing registration for that evening and nothing else.
	if r.FormValue("action") == "decline" {
		counts = regForm{Diet: store.DietOmnivore}
	}
	if problem := validateParty(v.Lang, counts); problem != "" {
		evenings, err := s.regularEvenings(ctx, world, g, v.Now)
		if err != nil {
			s.fail(w, r, "read the regular's evenings", err)
			return
		}
		s.renderRegular(w, r, v, g, evenings, g.form(), problem, http.StatusUnprocessableEntity)
		return
	}

	now := s.now()
	err = s.store.SaveRegistration(ctx, store.Registration{
		ID:            auth.ID(),
		Date:          d.Key,
		Kind:          store.KindGuest,
		Name:          st.Name,
		Host:          st.Host,
		Adults:        counts.Adults,
		Children:      counts.Children,
		Diet:          counts.Diet,
		Note:          counts.Note,
		StandingToken: st.Token,
		CreatedAt:     now, UpdatedAt: now,
		CreatedIP: s.clientIP(r),
	})
	if err != nil {
		s.fail(w, r, "save the regular's answer for one evening", err)
		return
	}
	s.log.Info("regular guest answered for one evening", "date", d.Key,
		"name", st.Name, "people", counts.Adults+counts.Children)
	http.Redirect(w, r, "/stamgast/"+st.Token+"?sparat=1", http.StatusSeeOther)
}

// revokeRegular takes a regular off the schedule for good: the evenings already
// sent keep what they were sent with, and everything from the next open one on
// is as if they had never asked.
func (s *Server) revokeRegular(ctx context.Context, ip string, g regular) error {
	for _, was := range g.Rows {
		if err := s.freezeClosedEvenings(ctx, ip, was); err != nil {
			return err
		}
	}
	return s.store.DeleteStandingByToken(ctx, g.first().Token)
}
