package web

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
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
}

// resolveList works out who is asking and about which evening. It answers the
// request itself and reports false when the caller should stop.
func (s *Server) resolveList(w http.ResponseWriter, r *http.Request) (*listRequest, bool) {
	ctx := r.Context()
	date := r.PathValue("date")
	role := s.guard.Role(r)
	viaKey := s.guard.CheckKey(listKeyPurpose, date, r.URL.Query().Get("nyckel"))

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
	return &listRequest{View: v, Dinner: d, Summary: sum, ViaKey: v.Bare}, true
}

// handleList serves the printable list. It is the one page that two very
// different callers reach: a member who is logged in, and a cooking-team
// leader who followed the signed link in their mail.
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	req, ok := s.resolveList(w, r)
	if !ok {
		return
	}
	v, d := req.View, req.Dinner
	v.Title = i18n.T(v.Lang, "list.title") + " " + i18n.DateLong(v.Lang, d.Date)

	// The download keeps whatever capability the reader arrived with, so a
	// leader following the mailed link can fetch the file too.
	csvPath := "/middag/" + d.Key + "/lista.csv"
	if req.ViaKey {
		csvPath += "?nyckel=" + url.QueryEscape(r.URL.Query().Get("nyckel"))
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
	}
	s.render(w, r, http.StatusOK, "list.html", v)
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
		})
	}
}

func ifYes(b bool, yes string) string {
	if b {
		return yes
	}
	return ""
}
