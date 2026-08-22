// Package web serves the dinner registration: server-rendered HTML, no build
// step, no client-side framework.
package web

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/auth"
	"github.com/O5ten/dinners/internal/config"
	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/mattermost"
	"github.com/O5ten/dinners/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server holds everything the handlers need.
type Server struct {
	cfg   *config.Config
	rt    config.Runtime
	store *store.Store
	guard *auth.Guard
	// mm is the bot that confirms a household's registration, messages the
	// cooking teams, and answers the pages' lookups of who is in the house.
	mm  *mattermost.Client
	log *slog.Logger
	// members caches the house's Mattermost directory, which the pickers offer
	// when a household says who it is and when a team is given a leader.
	members memberCache
	// tpl holds one parsed set per language. The language is baked into the
	// template functions, so a page can say {{t "key"}} and get the right
	// words without every call site passing a language around.
	tpl map[i18n.Lang]map[string]*template.Template
	now func() time.Time
}

// pages are the top-level templates. Each is parsed into its own set together
// with the shared layout, because every page defines a "content" block and Go
// templates share one namespace per set.
var pages = []string{
	"index.html", "login.html", "identity.html", "error.html", "dinner.html",
	"mine.html", "list.html", "guest.html", "admin.html",
}

// layouts are included in every page set.
var layouts = []string{"base.html", "fields.html"}

// New builds the HTTP server.
func New(cfg *config.Config, rt config.Runtime, st *store.Store, guard *auth.Guard, mm *mattermost.Client, log *slog.Logger) (*Server, error) {
	s := &Server{cfg: cfg, rt: rt, store: st, guard: guard, mm: mm, log: log, now: time.Now}
	s.tpl = make(map[i18n.Lang]map[string]*template.Template, len(i18n.Langs))
	for _, lang := range i18n.Langs {
		set := make(map[string]*template.Template, len(pages))
		for _, page := range pages {
			files := make([]string, 0, len(layouts)+1)
			for _, l := range layouts {
				files = append(files, "templates/"+l)
			}
			files = append(files, "templates/"+page)
			t, err := template.New(page).Funcs(s.funcs(lang)).ParseFS(templateFS, files...)
			if err != nil {
				return nil, fmt.Errorf("parse template %s (%s): %w", page, lang, err)
			}
			set[page] = t
		}
		s.tpl[lang] = set
	}
	return s, nil
}

// defaultLang is the language a visitor gets before choosing one.
func (s *Server) defaultLang() i18n.Lang {
	lang, _ := i18n.Parse(s.cfg.Site.Language)
	return lang
}

// Handler returns the router with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServer(http.FS(sub)))))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("POST /sprak", s.handleLanguage)

	// The guest page is the one thing that is deliberately not behind the
	// house password, so a visitor can be pointed straight at it.
	mux.HandleFunc("GET /gast", s.handleGuestForm)
	mux.HandleFunc("POST /gast", s.handleGuestSave)
	mux.HandleFunc("GET /gast/{token}", s.handleGuestEdit)
	mux.HandleFunc("POST /gast/{token}", s.handleGuestUpdate)
	// A guest gets no message from us at all, so the evening is handed to
	// their calendar from their own page, behind their own token.
	mux.HandleFunc("GET /gast/{token}/kalender.ics", s.handleGuestICS)

	// The printable list is reachable both by a logged-in member and by the
	// signed link sent to the cooking-team leader, so it does its own check.
	mux.HandleFunc("GET /middag/{date}/lista", s.handleList)
	mux.HandleFunc("GET /middag/{date}/lista.csv", s.handleListCSV)

	// Who is in the house, for the pickers on the identity form and in the
	// admin view. Behind the house password, like the pages that use it.
	mux.Handle("GET /medlemmar", s.member(s.handleMembers))

	mux.Handle("GET /jagar", s.member(s.handleIdentityForm))
	mux.Handle("POST /jagar", s.member(s.handleIdentitySave))
	mux.Handle("POST /glom-mig", s.member(s.handleForget))

	mux.Handle("GET /{$}", s.identified(s.handleIndex))
	mux.Handle("GET /middag/{date}", s.identified(s.handleDinner))
	mux.Handle("GET /middag/{date}/kalender.ics", s.member(s.handleDinnerICS))
	mux.Handle("POST /middag/{date}", s.identified(s.handleRegister))
	mux.Handle("GET /mina", s.identified(s.handleMine))
	mux.Handle("POST /stadigvarande", s.identified(s.handleStanding))

	mux.Handle("GET /admin", s.admin(s.handleAdmin))
	mux.Handle("POST /admin/lag", s.admin(s.handleAdminTeam))
	mux.Handle("POST /admin/sasong", s.admin(s.handleAdminSeason))
	mux.Handle("POST /admin/lag/ordning", s.admin(s.handleAdminOrder))
	mux.Handle("POST /admin/uppehall", s.admin(s.handleAdminBreak))
	mux.Handle("POST /admin/schema", s.admin(s.handleAdminSchedule))
	mux.Handle("POST /admin/installningar", s.admin(s.handleAdminSettings))
	mux.Handle("POST /admin/skicka", s.admin(s.handleAdminSend))
	mux.Handle("POST /admin/anmalan", s.admin(s.handleAdminDeleteRegistration))
	mux.Handle("GET /admin/export.csv", s.admin(s.handleAdminCSV))

	return s.recoverPanic(securityHeaders(mux))
}

// world is the state a request needs from the database before it can say
// anything about a dinner: the settings, the teams and the generated schedule.
type world struct {
	Settings store.Settings
	Teams    []store.Team
	Seasons  []store.Season
	Breaks   []store.Break
	Schedule dinner.Schedule
}

// defaults are the settings a database that has never been configured starts
// from; they come from config.yaml.
func (s *Server) defaults() store.Settings {
	return store.Settings{
		DeadlineWeekday:     s.cfg.Deadline.ParsedWeekday(),
		DeadlineMinutes:     s.cfg.Deadline.Minutes(),
		DeadlineWeeksBefore: s.cfg.Deadline.WeeksBefore,
		GuestOpen:           true,
	}
}

// world loads the settings, teams and seasons and builds the schedule. The
// tables are tiny — a handful of teams and a season or two — so this runs per
// request rather than being cached and going stale after an admin edit.
func (s *Server) world(ctx context.Context) (*world, error) {
	set, err := s.store.Settings(ctx, s.defaults())
	if err != nil {
		return nil, err
	}
	teams, err := s.store.Teams(ctx)
	if err != nil {
		return nil, err
	}
	seasons, err := s.store.Seasons(ctx)
	if err != nil {
		return nil, err
	}
	from, to := "0000-01-01", "9999-12-31"
	overrides, err := s.store.Overrides(ctx, from, to)
	if err != nil {
		return nil, err
	}
	breaks, err := s.store.Breaks(ctx)
	if err != nil {
		return nil, err
	}
	serving, _ := config.ParseClock(s.cfg.Dinner.ServingTime)
	return &world{
		Settings: set,
		Teams:    teams,
		Seasons:  seasons,
		Breaks:   breaks,
		Schedule: dinner.Build(seasons, teams, overrides, breaks, dinner.Params{
			Loc:            s.cfg.Location(),
			ServingMinutes: serving,
			Deadline: dinner.Deadline{
				Weekday:     set.DeadlineWeekday,
				Minutes:     set.DeadlineMinutes,
				WeeksBefore: set.DeadlineWeeksBefore,
			},
		}),
	}, nil
}

// guestOpen answers the one question the login page needs from the database.
// A failure here must not keep anybody out, so it errs towards offering the
// guest link and letting /gast turn the visitor away instead.
func (s *Server) guestOpen(ctx context.Context) bool {
	set, err := s.store.Settings(ctx, s.defaults())
	if err != nil {
		s.log.Error("read settings", "err", err)
		return true
	}
	return set.GuestOpen
}

// member wraps a handler so only someone with the house password reaches it.
func (s *Server) member(h func(http.ResponseWriter, *http.Request, *view)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := s.guard.Role(r)
		if !role.LoggedIn() {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		h(w, r, s.newView(r, role))
	})
}

// identified additionally insists that the member has said who they are. The
// Mattermost account is the identifier every registration hangs on, and it is
// where the confirmation goes, so there is nothing useful to show before it is
// known.
func (s *Server) identified(h func(http.ResponseWriter, *http.Request, *view)) http.Handler {
	return s.member(func(w http.ResponseWriter, r *http.Request, v *view) {
		if !v.Ident.Known() {
			http.Redirect(w, r, "/jagar?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		h(w, r, v)
	})
}

// admin wraps a handler so only the admin password reaches it.
func (s *Server) admin(h func(http.ResponseWriter, *http.Request, *view)) http.Handler {
	return s.member(func(w http.ResponseWriter, r *http.Request, v *view) {
		if !v.Role.Admin() {
			s.renderError(w, r, http.StatusForbidden,
				"Bara matgruppens administratör kommer åt den här sidan.",
				"Logga in igen med administratörslösenordet om du behöver ändra lag, säsong eller schema.")
			return
		}
		h(w, r, v)
	})
}

// view is the data every page shares.
type view struct {
	Site   config.Site
	Dinner config.Dinner
	// Lang is the language this page is rendered in, and Other the one the
	// switch in the top bar moves to.
	Lang  i18n.Lang
	Other i18n.Lang
	// Here is the address to come back to after switching language.
	Here     string
	Role     auth.Role
	Ident    auth.Identity
	Now      time.Time
	Loc      *time.Location
	Path     string
	Title    string
	BaseURL  string
	HasAdmin bool
	// ChatOn says the bot can really reach Mattermost. Without it nothing is
	// delivered and the site says so rather than pretending.
	ChatOn    bool
	GuestOpen bool
	// Bare drops the house navigation, for the printable list opened from a
	// link in a message by someone who is not logged in.
	Bare      bool
	Demo      bool
	DemoPass  string
	DemoAdmin string
	Flash     string
	FlashKind string
	Data      any
}

func (s *Server) newView(r *http.Request, role auth.Role) *view {
	lang := i18n.FromRequest(r, s.defaultLang())
	return &view{
		Site:      s.cfg.Site,
		Dinner:    s.cfg.Dinner,
		Lang:      lang,
		Other:     lang.Other(),
		Here:      r.URL.RequestURI(),
		Role:      role,
		Ident:     s.guard.Identity(r),
		Now:       s.now().In(s.cfg.Location()),
		Loc:       s.cfg.Location(),
		Path:      r.URL.Path,
		BaseURL:   s.rt.BaseURL,
		HasAdmin:  s.guard.HasAdmin(),
		ChatOn:    s.mm.Enabled(),
		GuestOpen: true,
		Demo:      s.rt.Demo,
		DemoPass:  s.rt.Password,
		DemoAdmin: s.rt.AdminPassword,
	}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, v *view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	set, ok := s.tpl[v.Lang]
	if !ok {
		set = s.tpl[i18n.Default]
	}
	t, ok := set[name]
	if !ok {
		s.log.Error("unknown template", "template", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Render into a buffer so a mid-template failure cannot emit half a page.
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, v); err != nil {
		s.log.Error("render template", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// errorPage renders the shared error page, translating the two phrases it is
// given by key so a handler never has to know the reader's language.
func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, status int, headlineKey, detailKey string) {
	lang := i18n.FromRequest(r, s.defaultLang())
	s.renderError(w, r, status, i18n.T(lang, headlineKey), i18n.T(lang, detailKey))
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, headline, detail string) {
	v := s.newView(r, s.guard.Role(r))
	v.Title = headline
	v.Data = map[string]any{"Headline": headline, "Detail": detail, "Status": status}
	s.render(w, r, status, "error.html", v)
}

// fail logs an unexpected error and shows the same page to the member either
// way, because there is nothing they can do about it.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.log.Error(what, "path", r.URL.Path, "err", err)
	s.errorPage(w, r, http.StatusInternalServerError,
		"error.wentwrong", "error.wentwrong.detail")
}

// funcs builds the template helpers for one language. Every helper that says
// something to a person is bound to that language here, so no template ever
// has to thread it through.
func (s *Server) funcs(lang i18n.Lang) template.FuncMap {
	return template.FuncMap{
		"t":             func(key string, args ...any) string { return i18n.T(lang, key, args...) },
		"weekday":       func(t time.Time) string { return i18n.Weekday(lang, t) },
		"weekdayName":   func(wd time.Weekday) string { return i18n.WeekdayName(lang, wd) },
		"weekdayPlural": func(wd time.Weekday) string { return i18n.WeekdayPlural(lang, wd) },
		"weekdayShort":  func(t time.Time) string { return i18n.WeekdayShort(lang, t) },
		"month":         func(t time.Time) string { return i18n.Month(lang, t) },
		"dateLong":      func(t time.Time) string { return i18n.DateLong(lang, t) },
		"dateLongYear":  func(t time.Time) string { return i18n.DateLongYear(lang, t) },
		"dateShort":     func(t time.Time) string { return i18n.DateShort(lang, t) },
		"dateTime":      func(t time.Time) string { return i18n.DateTime(lang, t) },
		"clock":         i18n.Clock,
		"titleCase":     i18n.TitleCase,
		"countdown":     func(until, now time.Time) string { return i18n.Countdown(lang, until, now) },
		"count":         func(unit string, n int) string { return i18n.Count(lang, unit, n) },
		"plural":        func(unit string, n int) string { return i18n.Plural(lang, unit, n) },
		"people":        func(n int) string { return i18n.Count(lang, "person", n) },
		"joinWeekdays":  func(wd []time.Weekday) string { return i18n.JoinWeekdays(lang, wd) },
		"diets":         func() []store.Diet { return store.Diets },
		"dietLabel":     func(d store.Diet) string { return DietLabel(lang, d) },
		"dietHint":      func(d store.Diet) string { return DietHint(lang, d) },
		"clockMinutes":  config.FormatClock,
		"add1":          func(i int) int { return i + 1 },
		"weekdayNum":    func(wd time.Weekday) int { return int(wd) },
		"hasWeekday": func(list []time.Weekday, wd time.Weekday) bool {
			for _, d := range list {
				if d == wd {
					return true
				}
			}
			return false
		},
		"dict":      dict,
		"hasPrefix": strings.HasPrefix,
	}
}

func dict(pairs ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			continue
		}
		m[key] = pairs[i+1]
	}
	return m
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		// Everything is served from this origin; no external scripts or styles.
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic serving request", "path", r.URL.Path, "err", rec)
				s.renderError(w, r, http.StatusInternalServerError,
					"Något gick fel", "Försök igen, eller hör av dig till husets datorgrupp om det fortsätter.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the caller's address, honouring X-Forwarded-For when the
// deployment sits behind a reverse proxy.
func (s *Server) clientIP(r *http.Request) string {
	if s.rt.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}
