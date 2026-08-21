package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/O5ten/dinners/internal/auth"
	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/store"
)

// The public guest page is the only thing outside the password gate. A visitor
// is given the address and registers themselves; they never see the house's
// list, only their own answer.

// guestForm is what a visitor fills in.
type guestForm struct {
	Date     string
	Name     string
	Host     string
	Email    string
	Adults   int
	Children int
	Diet     store.Diet
	Note     string
}

func (s *Server) guestView(r *http.Request) *view {
	v := s.newView(r, s.guard.Role(r))
	v.Title = i18n.T(v.Lang, "guest.title")
	return v
}

// openDinners are the evenings a guest may still register for.
func openDinners(world *world, v *view) []dinner.Dinner {
	var out []dinner.Dinner
	for _, d := range world.Schedule.Upcoming(v.Now, 0) {
		if d.Open(v.Now) {
			out = append(out, d)
		}
	}
	return out
}

func (s *Server) handleGuestForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	v := s.guestView(r)
	v.GuestOpen = world.Settings.GuestOpen
	if !world.Settings.GuestOpen {
		s.errorPage(w, r, http.StatusNotFound,
			"error.guest.closed", "error.guest.closed.detail")
		return
	}
	open := openDinners(world, v)
	v.Data = map[string]any{
		"Dinners": open,
		"Form": guestForm{
			Adults: 1, Diet: store.DietOmnivore, Date: r.URL.Query().Get("datum"),
		},
	}
	s.render(w, r, http.StatusOK, "guest.html", v)
}

func (s *Server) handleGuestSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	v := s.guestView(r)
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

	ip := s.clientIP(r)
	now := s.now()
	counts := readForm(r)
	form := guestForm{
		Date:   strings.TrimSpace(r.FormValue("date")),
		Name:   strings.TrimSpace(r.FormValue("name")),
		Host:   strings.TrimSpace(r.FormValue("host")),
		Email:  auth.NormalizeEmail(r.FormValue("email")),
		Adults: counts.Adults, Children: counts.Children,
		Diet: counts.Diet, Note: counts.Note,
	}

	reject := func(problem string, status int) {
		v.Data = map[string]any{"Dinners": openDinners(world, v), "Form": form, "Error": problem}
		s.render(w, r, status, "guest.html", v)
	}

	if !guestThrottle.allow(ip, now) {
		reject(i18n.T(v.Lang, "guest.throttled"), http.StatusTooManyRequests)
		return
	}

	d, ok := world.Schedule.Find(form.Date)
	switch {
	case !ok:
		reject(i18n.T(v.Lang, "guest.pickdinner"), http.StatusUnprocessableEntity)
		return
	case !d.Open(v.Now):
		reject(i18n.T(v.Lang, "guest.closed"), http.StatusUnprocessableEntity)
		return
	case form.Name == "":
		reject(i18n.T(v.Lang, "guest.needname"), http.StatusUnprocessableEntity)
		return
	case form.Email != "" && !auth.ValidEmail(form.Email):
		reject(i18n.T(v.Lang, "guest.bademail"), http.StatusUnprocessableEntity)
		return
	case counts.Adults+counts.Children <= 0:
		reject(i18n.T(v.Lang, "guest.atleastone"), http.StatusUnprocessableEntity)
		return
	}
	if problem := validateParty(v.Lang, counts); problem != "" {
		reject(problem, http.StatusUnprocessableEntity)
		return
	}

	guestThrottle.fail(ip, now)
	token := auth.Token()
	err = s.store.SaveRegistration(ctx, store.Registration{
		ID:     auth.ID(),
		Date:   d.Key,
		Kind:   store.KindGuest,
		Email:  form.Email,
		Name:   form.Name,
		Host:   form.Host,
		Adults: form.Adults, Children: form.Children,
		Diet:      form.Diet,
		Note:      form.Note,
		Token:     token,
		CreatedAt: now, UpdatedAt: now,
		CreatedIP: ip,
	})
	if err != nil {
		s.fail(w, r, "save guest registration", err)
		return
	}
	s.log.Info("guest registration", "date", d.Key, "people", form.Adults+form.Children)
	http.Redirect(w, r, "/gast/"+token+"?sparat=1", http.StatusSeeOther)
}

// handleGuestEdit shows a visitor their own registration again, so they can
// change the numbers or withdraw. The token in the address is the only thing
// that identifies them, which is why it never appears in anyone else's view.
func (s *Server) handleGuestEdit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reg, err := s.store.RegistrationByToken(ctx, r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		s.errorPage(w, r, http.StatusNotFound,
			"error.reg.notfound", "error.reg.notfound.detail")
		return
	}
	if err != nil {
		s.fail(w, r, "read guest registration", err)
		return
	}
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	v := s.guestView(r)
	v.GuestOpen = world.Settings.GuestOpen
	d, ok := world.Schedule.Find(reg.Date)
	if !ok {
		s.errorPage(w, r, http.StatusNotFound,
			"error.dinner.gone", "error.dinner.gone.detail")
		return
	}
	v.Title = i18n.T(v.Lang, "guest.yours")
	v.Data = map[string]any{
		"Dinner": d,
		"Token":  reg.Token,
		"Open":   d.Open(v.Now),
		"Saved":  r.URL.Query().Get("sparat") != "",
		"Form": guestForm{
			Date: reg.Date, Name: reg.Name, Host: reg.Host, Email: reg.Email,
			Adults: reg.Adults, Children: reg.Children,
			Diet: reg.Diet, Note: reg.Note,
		},
	}
	s.render(w, r, http.StatusOK, "guest.html", v)
}

func (s *Server) handleGuestUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reg, err := s.store.RegistrationByToken(ctx, r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		s.errorPage(w, r, http.StatusNotFound,
			"error.reg.notfound", "error.reg.notfound.detail")
		return
	}
	if err != nil {
		s.fail(w, r, "read guest registration", err)
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
	v := s.guestView(r)
	v.GuestOpen = world.Settings.GuestOpen
	d, ok := world.Schedule.Find(reg.Date)
	if !ok || !d.Open(v.Now) {
		s.errorPage(w, r, http.StatusConflict,
			"error.reg.closed", "error.reg.closed.detail")
		return
	}

	if r.FormValue("action") == "delete" {
		if err := s.store.DeleteRegistration(ctx, reg.ID); err != nil {
			s.fail(w, r, "delete guest registration", err)
			return
		}
		s.log.Info("guest registration withdrawn", "date", reg.Date)
		v.Title = i18n.T(v.Lang, "guest.removed")
		v.Data = map[string]any{"Dinner": d, "Removed": true}
		s.render(w, r, http.StatusOK, "guest.html", v)
		return
	}

	counts := readForm(r)
	form := guestForm{
		Date:   reg.Date,
		Name:   strings.TrimSpace(r.FormValue("name")),
		Host:   strings.TrimSpace(r.FormValue("host")),
		Email:  auth.NormalizeEmail(r.FormValue("email")),
		Adults: counts.Adults, Children: counts.Children,
		Diet: counts.Diet, Note: counts.Note,
	}
	reject := func(problem string) {
		v.Title = i18n.T(v.Lang, "guest.yours")
		v.Data = map[string]any{"Dinner": d, "Token": reg.Token, "Open": true,
			"Form": form, "Error": problem}
		s.render(w, r, http.StatusUnprocessableEntity, "guest.html", v)
	}
	switch {
	case form.Name == "":
		reject(i18n.T(v.Lang, "guest.needname"))
		return
	case form.Email != "" && !auth.ValidEmail(form.Email):
		reject(i18n.T(v.Lang, "guest.bademail"))
		return
	case counts.Adults+counts.Children <= 0:
		reject(i18n.T(v.Lang, "guest.atleastone.or"))
		return
	}
	if problem := validateParty(v.Lang, counts); problem != "" {
		reject(problem)
		return
	}

	reg.Name, reg.Host, reg.Email = form.Name, form.Host, form.Email
	reg.Adults, reg.Children = form.Adults, form.Children
	reg.Diet = form.Diet
	reg.Note, reg.UpdatedAt = form.Note, s.now()
	if err := s.store.SaveRegistration(ctx, reg); err != nil {
		s.fail(w, r, "update guest registration", err)
		return
	}
	http.Redirect(w, r, "/gast/"+reg.Token+"?sparat=1", http.StatusSeeOther)
}
