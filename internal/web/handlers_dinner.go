package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/auth"
	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/store"
)

// upcomingOnIndex is how many evenings the start page shows before sending
// people to the full season.
const upcomingOnIndex = 8

// summarize resolves the registrations for a batch of dinners in two queries
// rather than two per evening.
func (s *Server) summarize(ctx context.Context, dinners []dinner.Dinner) (map[string]dinner.Summary, error) {
	out := make(map[string]dinner.Summary, len(dinners))
	if len(dinners) == 0 {
		return out, nil
	}
	from, to := dinners[0].Key, dinners[0].Key
	for _, d := range dinners {
		if d.Key < from {
			from = d.Key
		}
		if d.Key > to {
			to = d.Key
		}
	}
	regs, err := s.store.RegistrationsBetween(ctx, from, to)
	if err != nil {
		return nil, err
	}
	byDate := map[string][]store.Registration{}
	for _, r := range regs {
		byDate[r.Date] = append(byDate[r.Date], r)
	}
	standing, err := s.store.AllStanding(ctx)
	if err != nil {
		return nil, err
	}
	byWeekday := map[time.Weekday][]store.Standing{}
	for _, st := range standing {
		byWeekday[st.Weekday] = append(byWeekday[st.Weekday], st)
	}
	for _, d := range dinners {
		out[d.Key] = dinner.Resolve(byDate[d.Key], byWeekday[d.Weekday()])
	}
	return out, nil
}

// summary resolves a single evening.
func (s *Server) summary(ctx context.Context, d dinner.Dinner) (dinner.Summary, error) {
	regs, err := s.store.Registrations(ctx, d.Key)
	if err != nil {
		return dinner.Summary{}, err
	}
	standing, err := s.store.StandingFor(ctx, d.Weekday())
	if err != nil {
		return dinner.Summary{}, err
	}
	return dinner.Resolve(regs, standing), nil
}

// row is one evening on the start page, together with what this household has
// answered for it.
type row struct {
	Dinner  dinner.Dinner
	Summary dinner.Summary
	// Mine is this household's line in the list, if it has one.
	Mine *dinner.Attendee
	Open bool
	// Break, when set, makes this row a pause in the season rather than an
	// evening: a school holiday or a red day with no dinner.
	Break *store.Break
}

// Answered reports whether the household has said something for this evening,
// rather than merely being carried along by its standing registration.
func (r row) Answered() bool { return r.Mine != nil && !r.Mine.Standing }

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	v.GuestOpen = world.Settings.GuestOpen

	upcoming := world.Schedule.Upcoming(v.Now, upcomingOnIndex)
	summaries, err := s.summarize(ctx, upcoming)
	if err != nil {
		s.fail(w, r, "summarize dinners", err)
		return
	}

	// Breaks are interleaved so that "why is there no dinner next week?" is
	// answered in place rather than left to guesswork.
	rows := make([]row, 0, len(upcoming))
	previous := v.Now.Format("2006-01-02")
	for _, d := range upcoming {
		for _, b := range world.Schedule.BreaksBetween(previous, d.Key) {
			pause := b
			rows = append(rows, row{Break: &pause})
		}
		previous = d.Key
		sum := summaries[d.Key]
		rows = append(rows, row{
			Dinner:  d,
			Summary: sum,
			Mine:    findMine(sum, v.Ident.Email),
			Open:    d.Open(v.Now),
		})
	}

	v.Title = "Middagar"
	v.Data = map[string]any{
		"Rows":       rows,
		"Season":     currentSeason(world, v.Now),
		"Total":      len(world.Schedule.Dinners),
		"NoSchedule": len(world.Schedule.Dinners) == 0,
	}
	s.render(w, r, http.StatusOK, "index.html", v)
}

// findMine picks this household's own line out of a summary, whether they are
// coming or have said no.
func findMine(sum dinner.Summary, email string) *dinner.Attendee {
	if email == "" {
		return nil
	}
	for i, a := range sum.Attendees {
		if a.Email == email && !a.Guest {
			return &sum.Attendees[i]
		}
	}
	for i, a := range sum.Declined {
		if a.Email == email && !a.Guest {
			return &sum.Declined[i]
		}
	}
	return nil
}

// currentSeason returns the season now falls in, or the next one to start.
func currentSeason(world *world, now time.Time) *store.Season {
	today := now.Format("2006-01-02")
	var next *store.Season
	for i, se := range world.Seasons {
		if se.Start <= today && today <= se.End {
			return &world.Seasons[i]
		}
		if se.Start > today && (next == nil || se.Start < next.Start) {
			next = &world.Seasons[i]
		}
	}
	return next
}

func (s *Server) handleDinner(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	v.GuestOpen = world.Settings.GuestOpen

	d, ok := world.Schedule.Find(r.PathValue("date"))
	if !ok {
		s.errorPage(w, r, http.StatusNotFound,
			"error.nodinner", "error.nodinner.detail")
		return
	}
	sum, err := s.summary(ctx, d)
	if err != nil {
		s.fail(w, r, "summarize dinner", err)
		return
	}

	form := s.formFor(ctx, d, v.Ident)
	s.renderDinner(w, r, v, world, d, sum, form, "", http.StatusOK)
}

// regForm is the registration form's fields, kept as typed values so a
// rejected submission can be shown back with what the member wrote.
type regForm struct {
	Adults   int
	Children int
	Diet     store.Diet
	Note     string
	// Answered marks a household that has registered for this evening, as
	// opposed to seeing its standing registration pre-filled.
	Answered bool
	// FromStanding marks the numbers as coming from the standing registration.
	FromStanding bool
}

// formFor pre-fills the registration form: with this evening's answer if there
// is one, otherwise with the household's standing registration for the
// weekday, otherwise with a plausible first guess.
func (s *Server) formFor(ctx context.Context, d dinner.Dinner, id auth.Identity) regForm {
	if reg, err := s.store.MemberRegistration(ctx, d.Key, id.Email); err == nil {
		return regForm{
			Adults: reg.Adults, Children: reg.Children, Diet: reg.Diet,
			Note: reg.Note, Answered: true,
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		s.log.Error("read registration", "date", d.Key, "err", err)
	}
	if list, err := s.store.StandingByEmail(ctx, id.Email); err == nil {
		for _, st := range list {
			if st.Weekday == d.Weekday() {
				return regForm{
					Adults: st.Adults, Children: st.Children, Diet: st.Diet,
					Note: st.Note, FromStanding: true,
				}
			}
		}
	}
	return regForm{Adults: 1, Diet: store.DietOmnivore}
}

func (s *Server) renderDinner(w http.ResponseWriter, r *http.Request, v *view,
	world *world, d dinner.Dinner, sum dinner.Summary, form regForm, problem string, status int) {

	v.Title = i18n.TitleCase(i18n.DateLong(v.Lang, d.Date))
	v.Data = map[string]any{
		"Dinner":  d,
		"Summary": sum,
		"Form":    form,
		"Error":   problem,
		"Mine":    findMine(sum, v.Ident.Email),
		"Open":    d.Open(v.Now),
		"Closed":  d.Closed(v.Now),
		"Over":    d.Over(v.Now),
		"Saved":   r.URL.Query().Get("sparat") != "",
		"ListURL": "/middag/" + d.Key + "/lista",
	}
	s.render(w, r, status, "dinner.html", v)
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	d, ok := world.Schedule.Find(r.PathValue("date"))
	if !ok {
		s.errorPage(w, r, http.StatusNotFound,
			"error.nodinner", "error.nodinner.check")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}

	form := readForm(r)
	form.Answered = true
	// "Vi kommer inte" is the same thing as a registration for nobody: it
	// overrides the household's standing registration for this evening only.
	declined := r.FormValue("action") == "decline"
	if declined {
		form = regForm{Answered: true, Diet: store.DietOmnivore}
	}

	reject := func(problem string) {
		sum, err := s.summary(ctx, d)
		if err != nil {
			s.fail(w, r, "summarize dinner", err)
			return
		}
		s.renderDinner(w, r, v, world, d, sum, form, problem, http.StatusUnprocessableEntity)
	}

	if !d.Open(v.Now) {
		if d.Cancelled {
			reject(i18n.T(v.Lang, "register.cancelled"))
		} else {
			reject(i18n.T(v.Lang, "register.closed", i18n.DateTime(v.Lang, d.Closes)))
		}
		return
	}
	if problem := validateParty(v.Lang, form); problem != "" {
		reject(problem)
		return
	}

	now := s.now()
	err = s.store.SaveRegistration(ctx, store.Registration{
		ID:        auth.ID(),
		Date:      d.Key,
		Kind:      store.KindMember,
		Email:     v.Ident.Email,
		Name:      v.Ident.Name,
		Apartment: v.Ident.Apartment,
		Adults:    form.Adults,
		Children:  form.Children,
		Diet:      form.Diet,
		Note:      form.Note,
		Token:     auth.Token(),
		CreatedAt: now, UpdatedAt: now,
		CreatedIP: s.clientIP(r),
	})
	if err != nil {
		s.fail(w, r, "save registration", err)
		return
	}
	s.log.Info("registration saved", "date", d.Key,
		"people", form.Adults+form.Children, "declined", declined)
	http.Redirect(w, r, "/middag/"+d.Key+"?sparat=1", http.StatusSeeOther)
}

// readForm reads the shared count and diet fields off a submitted form.
//
// The diet is kept exactly as submitted rather than coerced to a known value,
// so validation can refuse it. Quietly turning something unrecognised into the
// unrestricted meal would record a vegan as eating everything, which is the
// one mistake here with real consequences.
func readForm(r *http.Request) regForm {
	return regForm{
		Adults:   formInt(r, "adults"),
		Children: formInt(r, "children"),
		Diet:     store.Diet(strings.TrimSpace(r.FormValue("diet"))),
		Note:     strings.TrimSpace(r.FormValue("note")),
	}
}

// maxPeople is a sanity bound. A household of twenty is already implausible;
// the point is to stop a typo turning into three hundred portions.
const maxPeople = 20

// maxNote bounds the free-text restriction, which is printed on the list.
const maxNote = 300

// validateParty checks the numbers and the diet, and returns the complaint in
// the reader's language, or an empty string when all is well.
func validateParty(lang i18n.Lang, f regForm) string {
	switch {
	case f.Adults < 0 || f.Children < 0:
		return i18n.T(lang, "register.negative")
	case f.Adults > maxPeople || f.Children > maxPeople:
		return i18n.T(lang, "register.toomany", maxPeople)
	case !f.Diet.Valid():
		return i18n.T(lang, "register.nodiet")
	case len([]rune(f.Note)) > maxNote:
		return i18n.T(lang, "register.longnote", maxNote)
	}
	return ""
}

func formInt(r *http.Request, name string) int {
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue(name)))
	if err != nil {
		return 0
	}
	return n
}
