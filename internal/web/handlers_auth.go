package web

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/O5ten/dinners/internal/auth"
)

// loginThrottle slows down password guessing from a single address. It is a
// small in-memory counter, which is plenty for a house of forty people.
type loginThrottle struct {
	mu     sync.Mutex
	tries  map[string][]time.Time
	window time.Duration
	maxTry int
}

func newThrottle(maxTry int) *loginThrottle {
	return &loginThrottle{tries: map[string][]time.Time{}, window: 15 * time.Minute, maxTry: maxTry}
}

// allow reports whether another attempt from ip may be made.
func (t *loginThrottle) allow(ip string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	cut := now.Add(-t.window)
	kept := t.tries[ip][:0]
	for _, at := range t.tries[ip] {
		if at.After(cut) {
			kept = append(kept, at)
		}
	}
	t.tries[ip] = kept
	return len(kept) < t.maxTry
}

func (t *loginThrottle) fail(ip string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tries[ip] = append(t.tries[ip], now)
}

func (t *loginThrottle) reset(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.tries, ip)
}

var throttle = newThrottle(10)

// guestThrottle bounds how much one address can post to the open guest page.
// It is not a password gate, just a brake on someone filling the list with
// nonsense.
var guestThrottle = newThrottle(20)

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	role := s.guard.Role(r)
	if role.LoggedIn() {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	v := s.newView(r, role)
	v.Title = "Logga in"
	v.GuestOpen = s.guestOpen(r.Context())
	v.Data = map[string]any{"Next": r.URL.Query().Get("next")}
	s.render(w, r, http.StatusOK, "login.html", v)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Formuläret kunde inte läsas", "Försök igen.")
		return
	}
	ip := s.clientIP(r)
	now := s.now()
	next := safeNext(r.FormValue("next"))

	v := s.newView(r, "")
	v.Title = "Logga in"
	v.GuestOpen = s.guestOpen(r.Context())

	if !throttle.allow(ip, now) {
		v.Data = map[string]any{"Next": r.FormValue("next"),
			"Error": "För många försök. Vänta en kvart och prova igen."}
		s.render(w, r, http.StatusTooManyRequests, "login.html", v)
		return
	}

	role := s.guard.Check(strings.TrimSpace(r.FormValue("password")))
	if !role.LoggedIn() {
		throttle.fail(ip, now)
		s.log.Warn("failed login", "ip", ip)
		v.Data = map[string]any{"Next": r.FormValue("next"), "Error": "Fel lösenord."}
		s.render(w, r, http.StatusUnauthorized, "login.html", v)
		return
	}
	throttle.reset(ip)
	s.guard.Issue(w, role)
	s.log.Info("login", "role", role, "ip", ip)
	// Somebody who has not said who they are yet is sent to do that first, and
	// then on to wherever they were heading.
	if !s.guard.Identity(r).Known() {
		http.Redirect(w, r, "/jagar?next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.guard.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleIdentityForm asks who the member is. The e-mail address is what ties
// a registration to a household, so it is the one thing that is required.
func (s *Server) handleIdentityForm(w http.ResponseWriter, r *http.Request, v *view) {
	v.Title = "Vem är du?"
	v.Data = map[string]any{
		"Next":  r.URL.Query().Get("next"),
		"Form":  v.Ident,
		"Known": v.Ident.Known(),
	}
	s.render(w, r, http.StatusOK, "identity.html", v)
}

func (s *Server) handleIdentitySave(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Formuläret kunde inte läsas", "Försök igen.")
		return
	}
	id := auth.Identity{
		Name:      strings.TrimSpace(r.FormValue("name")),
		Apartment: strings.TrimSpace(r.FormValue("apartment")),
		Email:     auth.NormalizeEmail(r.FormValue("email")),
	}
	var problem string
	switch {
	case id.Name == "":
		problem = "Skriv ditt namn, så vet matlaget vem som kommer."
	case !auth.ValidEmail(id.Email):
		problem = "Skriv en e-postadress som fungerar, till exempel anna@example.se."
	}
	if problem != "" {
		v.Title = "Vem är du?"
		v.Data = map[string]any{"Next": r.FormValue("next"), "Form": id, "Error": problem}
		s.render(w, r, http.StatusUnprocessableEntity, "identity.html", v)
		return
	}
	s.guard.Remember(w, id)
	s.log.Info("identity set", "name", id.Name)
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

// handleForget drops the remembered identity, for a shared computer.
func (s *Server) handleForget(w http.ResponseWriter, r *http.Request, v *view) {
	s.guard.Forget(w)
	s.guard.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext keeps redirects on this site.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}
