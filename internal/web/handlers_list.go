package web

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/auth"
	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/store"
)

// listKeyPurpose namespaces the capability that opens one evening's list.
const listKeyPurpose = "lista"

// listKeyTTL is how long a mailed link keeps working. A season is a few
// months, and the leader may well open the mail again afterwards.
const listKeyTTL = 365 * 24 * time.Hour

// listURL is the absolute address mailed to the cooking-team leader. The key
// in it grants access to that one evening's list and nothing else, so the
// leader does not have to dig out the house password to see what to cook.
func (s *Server) listURL(d dinner.Dinner) string {
	return s.rt.BaseURL + "/middag/" + d.Key + "/lista?nyckel=" + url.QueryEscape(s.listKey(d))
}

// listCSVURL is the same capability pointed at the export. It is what goes
// into a spreadsheet's IMPORTDATA, so it has to be absolute and has to carry
// its own key: the fetch is made by Google's servers, not by a logged-in
// browser.
func (s *Server) listCSVURL(d dinner.Dinner) string {
	return s.rt.BaseURL + "/middag/" + d.Key + "/lista.csv?nyckel=" + url.QueryEscape(s.listKey(d))
}

func (s *Server) listKey(d dinner.Dinner) string {
	return s.guard.Key(listKeyPurpose, d.Key, listKeyTTL)
}

// listRequest is one resolved request for an evening's list, shared by the page
// and the export so the two cannot drift apart on who is allowed to see what.
type listRequest struct {
	View    *view
	Dinner  dinner.Dinner
	Summary dinner.Summary
	// ViaKey means the reader followed the mailed link rather than logging in.
	ViaKey bool
	// Key is the signed key the reader presented, when it is good for this
	// evening, whether or not they are also logged in. It is carried on every
	// link and form the page offers, so the leader keeps their access.
	Key string
	// Team means the reader is the evening's cooking team: they came by the
	// key, or they are its leader or the administrator. Only the team may put
	// somebody on the list once registration has closed.
	Team bool
}

// resolveList works out who is asking and about which evening. It answers the
// request itself and reports false when the caller should stop.
func (s *Server) resolveList(w http.ResponseWriter, r *http.Request) (*listRequest, bool) {
	ctx := r.Context()
	date := r.PathValue("date")
	role := s.guard.Role(r)
	// FormValue rather than the query: the team's own forms post the key back.
	key := r.FormValue("nyckel")
	viaKey := s.guard.CheckKey(listKeyPurpose, date, key)
	if !viaKey {
		key = ""
	}

	if !role.LoggedIn() && !viaKey {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return nil, false
	}

	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return nil, false
	}
	d, ok := world.Schedule.Find(date)
	if !ok {
		s.errorPage(w, r, http.StatusNotFound, "error.nodinner", "error.nodinner.link")
		return nil, false
	}
	sum, err := s.summary(ctx, d)
	if err != nil {
		s.fail(w, r, "summarize dinner", err)
		return nil, false
	}

	v := s.newView(r, role)
	v.GuestOpen = world.Settings.GuestOpen
	v.Bare = viaKey && !role.LoggedIn()
	// A leader who is logged in and has said who they are is the team too,
	// without having to dig out the mail.
	leader := d.Team != nil && d.Team.LeaderUsername != "" &&
		store.Member(v.Ident.MMUsername) == d.Team.LeaderUsername
	return &listRequest{
		View: v, Dinner: d, Summary: sum, ViaKey: v.Bare, Key: key,
		Team: viaKey || leader || role.Admin(),
	}, true
}

// handleList serves the printable list. It is the one page that two very
// different callers reach: a member who is logged in, and a cooking-team
// leader who followed the signed link in their mail.
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	req, ok := s.resolveList(w, r)
	if !ok {
		return
	}
	s.renderList(w, r, req, lateForm{regForm: regForm{Adults: 1, Diet: store.DietOmnivore}}, "", http.StatusOK)
}

func (s *Server) renderList(w http.ResponseWriter, r *http.Request, req *listRequest,
	late lateForm, problem string, status int) {

	v, d := req.View, req.Dinner
	v.Title = i18n.T(v.Lang, "list.title") + " " + i18n.DateLong(v.Lang, d.Date)

	// The download keeps whatever capability the reader arrived with, so a
	// leader following the mailed link can fetch the file too.
	csvPath := "/middag/" + d.Key + "/lista.csv"
	if req.ViaKey {
		csvPath += "?nyckel=" + url.QueryEscape(req.Key)
	}

	v.Data = map[string]any{
		"Dinner":  d,
		"Summary": req.Summary,
		"Open":    d.Open(v.Now),
		"Closed":  d.Closed(v.Now),
		"Over":    d.Over(v.Now),
		// ViaKey means the reader followed the mailed link rather than logging
		// in, so the page leaves out the house navigation they cannot use.
		"ViaKey":  req.ViaKey,
		"CSVPath": csvPath,
		// The live link is only offered to the cooking team and the
		// administrator. Anybody in the house may read the list, but a link
		// that works without logging in is a different thing to hand out, and
		// only the team has a reason for one.
		"CSVLive": req.ViaKey || v.Role.Admin(),
		"CSVURL":  s.listCSVURL(d),
		// Team may take away the parties it added itself; AddLate is whether
		// it may add one right now.
		"Team":     req.Team,
		"AddLate":  req.Team && lateWindow(d, v.Now),
		"Key":      req.Key,
		"LateForm": late,
		"Error":    problem,
		"Added":    r.URL.Query().Get("tillagd") != "",
		"Removed":  r.URL.Query().Get("borttagen") != "",
	}
	s.render(w, r, status, "list.html", v)
}

// lateForm is the cooking team's form for somebody who forgot to register:
// the usual counts and diet, and a name, since there is no account to take
// one from.
type lateForm struct {
	regForm
	Name      string
	Apartment string
}

// maxLateName bounds the typed name and flat number, which are printed on the
// list.
const maxLateName = 100

// lateWindow is when the team may add to the list by hand: once households can
// no longer do it themselves, for as long as the evening is not cancelled.
// That includes after the meal, so the list ends up matching who actually ate.
func lateWindow(d dinner.Dinner, now time.Time) bool {
	return !d.Cancelled && !d.Open(now)
}

// listBack is the list's own address with what the reader came in with, so a
// leader who is not logged in lands on the page they were on.
func listBack(req *listRequest, flag string) string {
	q := url.Values{flag: {"1"}}
	if req.Key != "" {
		q.Set("nyckel", req.Key)
	}
	return "/middag/" + req.Dinner.Key + "/lista?" + q.Encode()
}

// handleListAdd lets the cooking team put somebody on the list after
// registration has closed. Households often forget and tell the leader
// instead; this is where the leader writes them in, so the totals and the
// season's records say who really ate.
func (s *Server) handleListAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	req, ok := s.resolveList(w, r)
	if !ok {
		return
	}
	v, d := req.View, req.Dinner
	if !req.Team {
		s.errorPage(w, r, http.StatusForbidden, "list.late.forbidden", "list.late.forbidden.detail")
		return
	}

	form := lateForm{
		regForm:   readForm(r),
		Name:      strings.TrimSpace(r.FormValue("name")),
		Apartment: strings.TrimSpace(r.FormValue("apartment")),
	}
	// Whoever forgot is counted as they are; there is no box for guests.
	form.Guests, form.GuestNames = 0, ""

	reject := func(problem string) {
		s.renderList(w, r, req, form, problem, http.StatusUnprocessableEntity)
	}
	switch {
	case d.Cancelled:
		reject(i18n.T(v.Lang, "register.cancelled"))
		return
	case d.Open(v.Now):
		reject(i18n.T(v.Lang, "list.late.stillopen"))
		return
	case form.Name == "":
		reject(i18n.T(v.Lang, "list.late.needname"))
		return
	case len([]rune(form.Name)) > maxLateName, len([]rune(form.Apartment)) > maxLateName:
		reject(i18n.T(v.Lang, "list.late.longname", maxLateName))
		return
	case form.Adults+form.Children <= 0:
		reject(i18n.T(v.Lang, "guest.atleastone"))
		return
	}
	if problem := validateParty(v.Lang, form.regForm); problem != "" {
		reject(problem)
		return
	}

	now := s.now()
	reg := store.Registration{
		ID:        auth.ID(),
		Date:      d.Key,
		Kind:      store.KindLate,
		Name:      form.Name,
		Apartment: form.Apartment,
		Adults:    form.Adults,
		Children:  form.Children,
		Diet:      form.Diet,
		Note:      form.Note,
		CreatedAt: now, UpdatedAt: now,
		CreatedIP: s.clientIP(r),
	}
	if err := s.store.SaveRegistration(r.Context(), reg); err != nil {
		s.fail(w, r, "save late registration", err)
		return
	}
	s.log.Info("late registration added by the cooking team", "date", d.Key,
		"people", form.Adults+form.Children, "by", lateAuthor(req))
	http.Redirect(w, r, listBack(req, "tillagd"), http.StatusSeeOther)
}

// handleListRemove takes back a party the cooking team added. The team may
// only remove what it put there itself: a household's own answer is theirs,
// and only the administrator overrules it.
func (s *Server) handleListRemove(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	req, ok := s.resolveList(w, r)
	if !ok {
		return
	}
	if !req.Team {
		s.errorPage(w, r, http.StatusForbidden, "list.late.forbidden", "list.late.forbidden.detail")
		return
	}
	ctx := r.Context()
	id := r.FormValue("id")
	regs, err := s.store.Registrations(ctx, req.Dinner.Key)
	if err != nil {
		s.fail(w, r, "read registrations", err)
		return
	}
	found := false
	for _, reg := range regs {
		if reg.ID == id && reg.Late() {
			found = true
			break
		}
	}
	if !found {
		s.errorPage(w, r, http.StatusNotFound, "list.late.notfound", "list.late.notfound.detail")
		return
	}
	if err := s.store.DeleteRegistration(ctx, id); err != nil {
		s.fail(w, r, "delete late registration", err)
		return
	}
	s.log.Info("late registration removed by the cooking team", "date", req.Dinner.Key,
		"id", id, "by", lateAuthor(req))
	http.Redirect(w, r, listBack(req, "borttagen"), http.StatusSeeOther)
}

// lateAuthor says in the log who changed the list by hand.
func lateAuthor(req *listRequest) string {
	switch {
	case req.View.Ident.MMUsername != "":
		return req.View.Ident.MMUsername
	case req.Key != "":
		return "the mailed link"
	}
	return "unknown"
}

// handleListCSV exports one evening's list as a spreadsheet.
//
// It is a single flat table with one row per household and nothing else — no
// totals row, no blank lines, no title — because the point is to be pasted or
// pulled into a sheet that already has its own formulas. Households that said
// no are left out: this is the list of who eats.
func (s *Server) handleListCSV(w http.ResponseWriter, r *http.Request) {
	req, ok := s.resolveList(w, r)
	if !ok {
		return
	}
	v, d, sum := req.View, req.Dinner, req.Summary
	lang := v.Lang

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="matlista-%s.csv"`, d.Key))
	// A spreadsheet that pulls this in with IMPORTDATA re-reads it whenever it
	// recalculates, and the numbers do change until registration closes.
	w.Header().Set("Cache-Control", "no-store")

	// Excel needs the byte-order mark to believe a CSV is UTF-8; without it
	// every å and ä in the house arrives mangled. Google Sheets ignores it.
	if _, err := w.Write([]byte("\ufeff")); err != nil {
		return
	}

	cw := csv.NewWriter(w)
	defer cw.Flush()
	cw.Write([]string{
		i18n.T(lang, "csv.date"),
		i18n.T(lang, "csv.name"),
		i18n.T(lang, "csv.apartment"),
		i18n.T(lang, "csv.adults"),
		i18n.T(lang, "csv.children"),
		i18n.T(lang, "csv.people"),
		i18n.T(lang, "csv.diet"),
		i18n.T(lang, "csv.allergies"),
		i18n.T(lang, "csv.guest"),
		i18n.T(lang, "csv.host"),
		i18n.T(lang, "csv.standing"),
		// Last, so the columns a spreadsheet already reads keep their places.
		i18n.T(lang, "csv.guests"),
	})
	yes := i18n.T(lang, "yes")
	for _, a := range sum.Attendees {
		cw.Write([]string{
			d.Key,
			a.Name,
			a.Apartment,
			strconv.Itoa(a.Adults),
			strconv.Itoa(a.Children),
			strconv.Itoa(a.People()),
			DietLabel(lang, a.Diet),
			a.Note,
			ifYes(a.Guest, yes),
			a.Host,
			ifYes(a.Standing, yes),
			strconv.Itoa(a.Guests),
		})
	}
}

func ifYes(b bool, yes string) string {
	if b {
		return yes
	}
	return ""
}
