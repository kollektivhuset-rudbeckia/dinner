package web

import (
	"net/http"
	"net/url"
	"time"

	"github.com/O5ten/dinners/internal/dinner"
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
	key := s.guard.Key(listKeyPurpose, d.Key, listKeyTTL)
	return s.rt.BaseURL + "/middag/" + d.Key + "/lista?nyckel=" + url.QueryEscape(key)
}

// handleList serves the printable list. It is the one page that two very
// different callers reach: a member who is logged in, and a cooking-team
// leader who followed the signed link in their mail.
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	date := r.PathValue("date")
	role := s.guard.Role(r)
	viaKey := s.guard.CheckKey(listKeyPurpose, date, r.URL.Query().Get("nyckel"))

	if !role.LoggedIn() && !viaKey {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}

	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	d, ok := world.Schedule.Find(date)
	if !ok {
		s.renderError(w, r, http.StatusNotFound, "Ingen middag den dagen",
			"Kontrollera datumet i länken.")
		return
	}
	sum, err := s.summary(ctx, d)
	if err != nil {
		s.fail(w, r, "summarize dinner", err)
		return
	}

	v := s.newView(r, role)
	v.GuestOpen = world.Settings.GuestOpen
	v.Bare = viaKey && !role.LoggedIn()
	v.Title = "Matlista " + DateLong(d.Date)
	v.Data = map[string]any{
		"Dinner":  d,
		"Summary": sum,
		"Open":    d.Open(v.Now),
		"Closed":  d.Closed(v.Now),
		"Over":    d.Over(v.Now),
		// ViaKey means the reader followed the mailed link rather than logging
		// in, so the page leaves out the house navigation they cannot use.
		"ViaKey": viaKey && !role.LoggedIn(),
	}
	s.render(w, r, http.StatusOK, "list.html", v)
}
