package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/ical"
	"github.com/O5ten/dinners/internal/store"
)

// mealLength is how long an evening is put in the calendar for. A dinner has a
// serving time but no end: people leave when they leave. An hour and a half is
// long enough that the entry reads as an evening rather than a moment.
const mealLength = 90 * time.Minute

// calendar is the "add to calendar" offer for one evening: the file for the
// calendars that read files, and a link each for the two web calendars people
// actually use.
type calendar struct {
	ICS     string
	Google  string
	Outlook string
}

// event renders a dinner as a calendar event, in the language of whoever the
// file is being handed to.
func (s *Server) event(d dinner.Dinner, lang i18n.Lang) ical.Event {
	loc := s.cfg.Location()
	start := d.Serving.In(loc)
	summary := i18n.T(lang, "ical.summary", s.cfg.Site.HouseName)
	if s.cfg.Site.HouseName == "" {
		summary = i18n.T(lang, "ical.summary.plain")
	}

	var desc []string
	if d.Team != nil {
		desc = append(desc, i18n.T(lang, "ical.cooks", d.Team.Label()))
	}
	if d.Note != "" {
		desc = append(desc, d.Note)
	}
	desc = append(desc, i18n.T(lang, "ical.change", s.rt.BaseURL+"/middag/"+d.Key))

	return ical.Event{
		UID:         "middag-" + d.Key + "@dinner.rudbeckia.nu",
		Summary:     summary,
		Description: strings.Join(desc, "\n"),
		Location:    s.cfg.Dinner.Location,
		Start:       start,
		End:         start.Add(mealLength),
		URL:         s.rt.BaseURL + "/middag/" + d.Key,
		Cancelled:   d.Cancelled,
		Timezone:    s.cfg.Site.Timezone,
	}
}

// guestEvent is the same evening as a guest sees it: their own link back
// instead of the house's page they cannot open, and nothing about the house
// beyond when and where the food is — which is all the guest page ever shows.
func (s *Server) guestEvent(d dinner.Dinner, token string, lang i18n.Lang) ical.Event {
	ev := s.event(d, lang)
	link := s.rt.BaseURL + "/gast/" + token
	ev.URL = link
	ev.Description = i18n.T(lang, "ical.change", link)
	return ev
}

// calendarLinks builds the three ways to keep an evening, for a page.
func (s *Server) calendarLinks(ev ical.Event, ics string) calendar {
	return calendar{
		ICS:     ics,
		Google:  ical.GoogleLink(ev),
		Outlook: ical.OutlookLink(ev),
	}
}

// writeICS hands one event over as a downloadable file.
func (s *Server) writeICS(w http.ResponseWriter, d dinner.Dinner, ev ical.Event) {
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+
		ical.Filename(d.Date.In(s.cfg.Location()))+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(ical.Calendar(s.cfg.Site.Title, []ical.Event{ev}, s.now()))
}

// handleDinnerICS is the file behind the member's "add to calendar" button.
func (s *Server) handleDinnerICS(w http.ResponseWriter, r *http.Request, v *view) {
	world, err := s.world(r.Context())
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	d, ok := world.Schedule.Find(r.PathValue("date"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.writeICS(w, d, s.event(d, v.Lang))
}

// handleGuestICS is the same for a visitor, found by the token in their own
// link — the only thing that identifies them.
func (s *Server) handleGuestICS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reg, err := s.store.RegistrationByToken(ctx, r.PathValue("token"))
	if err != nil || reg.Kind != store.KindGuest {
		http.NotFound(w, r)
		return
	}
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	d, ok := world.Schedule.Find(reg.Date)
	if !ok {
		http.NotFound(w, r)
		return
	}
	lang := i18n.FromRequest(r, s.defaultLang())
	s.writeICS(w, d, s.guestEvent(d, reg.Token, lang))
}
