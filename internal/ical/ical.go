// Package ical renders a dinner as an iCalendar event and as "add to calendar"
// links. An .ics file covers Apple Calendar, Outlook, Thunderbird and Android;
// Google and Outlook on the web get a template URL each, because that is what
// people expect from a button.
//
// It is what replaces the confirmation e-mail nobody gets any more: the site
// hands the evening straight to the calendar the person already keeps.
package ical

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Event is the calendar view of one dinner.
type Event struct {
	UID         string
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	Created     time.Time
	URL         string
	Cancelled   bool
	// Timezone is the house's own timezone, written into the file so that a
	// calendar in another one still shows the right evening.
	Timezone string
}

const prodID = "-//Kollektivhuset Rudbeckia//Middagar//SV"

// Calendar renders one or more events as a complete .ics document.
func Calendar(name string, events []Event, now time.Time) []byte {
	var b strings.Builder
	write := func(line string) { b.WriteString(fold(line) + "\r\n") }

	write("BEGIN:VCALENDAR")
	write("VERSION:2.0")
	write("PRODID:" + prodID)
	write("CALSCALE:GREGORIAN")
	write("METHOD:PUBLISH")
	if name != "" {
		write("X-WR-CALNAME:" + escape(name))
	}
	if len(events) > 0 && events[0].Timezone != "" {
		write("X-WR-TIMEZONE:" + events[0].Timezone)
	}
	for _, e := range events {
		write("BEGIN:VEVENT")
		write("UID:" + e.UID)
		write("DTSTAMP:" + stamp(now))
		write("DTSTART:" + stamp(e.Start))
		write("DTEND:" + stamp(e.End))
		write("SUMMARY:" + escape(e.Summary))
		if e.Description != "" {
			write("DESCRIPTION:" + escape(e.Description))
		}
		if e.Location != "" {
			write("LOCATION:" + escape(e.Location))
		}
		if e.URL != "" {
			write("URL:" + e.URL)
		}
		if !e.Created.IsZero() {
			write("CREATED:" + stamp(e.Created))
		}
		if e.Cancelled {
			write("STATUS:CANCELLED")
		} else {
			write("STATUS:CONFIRMED")
		}
		write("TRANSP:OPAQUE")
		write("END:VEVENT")
	}
	write("END:VCALENDAR")
	return []byte(b.String())
}

// GoogleLink builds a Google Calendar "add event" URL.
func GoogleLink(e Event) string {
	q := url.Values{}
	q.Set("action", "TEMPLATE")
	q.Set("text", e.Summary)
	q.Set("dates", stamp(e.Start)+"/"+stamp(e.End))
	if e.Description != "" {
		q.Set("details", e.Description)
	}
	if e.Location != "" {
		q.Set("location", e.Location)
	}
	if e.Timezone != "" {
		q.Set("ctz", e.Timezone)
	}
	return "https://calendar.google.com/calendar/render?" + q.Encode()
}

// OutlookLink builds an Outlook Web "compose event" URL.
func OutlookLink(e Event) string {
	q := url.Values{}
	q.Set("path", "/calendar/action/compose")
	q.Set("rru", "addevent")
	q.Set("subject", e.Summary)
	q.Set("startdt", e.Start.UTC().Format(time.RFC3339))
	q.Set("enddt", e.End.UTC().Format(time.RFC3339))
	if e.Description != "" {
		q.Set("body", e.Description)
	}
	if e.Location != "" {
		q.Set("location", e.Location)
	}
	return "https://outlook.office.com/calendar/0/deeplink/compose?" + q.Encode()
}

// stamp writes an instant the way iCalendar spells UTC.
func stamp(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

func escape(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		";", `\;`,
		",", `\,`,
		"\r\n", `\n`,
		"\n", `\n`,
		"\r", `\n`,
	)
	return r.Replace(s)
}

// fold wraps long lines at 75 octets as RFC 5545 requires. The limit here is a
// little lower so that the leading space of a continuation still fits.
func fold(line string) string {
	const limit = 73
	if len(line) <= limit {
		return line
	}
	var b strings.Builder
	count := 0
	for i, r := range line {
		size := len(string(r))
		if count+size > limit && i > 0 {
			b.WriteString("\r\n ")
			count = 1
		}
		b.WriteRune(r)
		count += size
	}
	return b.String()
}

// Filename returns a safe .ics filename for one dinner.
func Filename(date time.Time) string {
	return fmt.Sprintf("middag-%s.ics", date.Format("2006-01-02"))
}
