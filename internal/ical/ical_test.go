package ical

import (
	"strings"
	"testing"
	"time"
)

var (
	stockholm = mustLoad("Europe/Stockholm")
	stamped   = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
)

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func evening() Event {
	start := time.Date(2026, 8, 25, 18, 0, 0, 0, stockholm)
	return Event{
		UID:         "middag-2026-08-25@example",
		Summary:     "Gemensam middag i Kollektivhuset Rudbeckia",
		Description: "Matlag: Lag 1 · Mikael Östberg",
		Location:    "stora matsalen",
		Start:       start,
		End:         start.Add(90 * time.Minute),
		URL:         "https://mat.example.se/middag/2026-08-25",
		Timezone:    "Europe/Stockholm",
	}
}

// The evening is written in UTC, which is what every calendar reads, and a
// summer evening in Stockholm is two hours ahead of it.
func TestCalendarWritesTheEveningInUTC(t *testing.T) {
	got := string(Calendar("Rudbeckia middagar", []Event{evening()}, stamped))
	for _, want := range []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"X-WR-CALNAME:Rudbeckia middagar",
		"X-WR-TIMEZONE:Europe/Stockholm",
		"UID:middag-2026-08-25@example",
		"DTSTAMP:20260820T120000Z",
		"DTSTART:20260825T160000Z",
		"DTEND:20260825T173000Z",
		"STATUS:CONFIRMED",
		"END:VCALENDAR",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the file is missing %q:\n%s", want, got)
		}
	}
	// Every line ends the way the format says, including the last.
	if !strings.HasSuffix(got, "END:VCALENDAR\r\n") {
		t.Error("lines should end with CRLF")
	}
}

// A cancelled evening is still an event: it is how a calendar that already held
// the dinner is told to let go of it.
func TestACancelledEveningSaysSo(t *testing.T) {
	ev := evening()
	ev.Cancelled = true
	if got := string(Calendar("", []Event{ev}, stamped)); !strings.Contains(got, "STATUS:CANCELLED") {
		t.Errorf("= %s", got)
	}
}

// Commas and semicolons separate fields in iCalendar, so a note that contains
// them has to be escaped rather than split the line in two.
func TestTextIsEscaped(t *testing.T) {
	ev := evening()
	ev.Description = "Lag 1; Mikael, Anna\nAndra våningen"
	got := string(Calendar("", []Event{ev}, stamped))
	if !strings.Contains(got, `Lag 1\; Mikael\, Anna\nAndra`) {
		t.Errorf("the description is not escaped:\n%s", got)
	}
	if strings.Contains(got, "Anna\r\nAndra") {
		t.Error("a newline in the text must not end the line")
	}
}

// Long lines are folded, and a continuation begins with a space. Unfolding the
// file has to give the text back unchanged, accents and all.
func TestLongLinesFoldAndUnfold(t *testing.T) {
	ev := evening()
	ev.Description = strings.Repeat("Kollektivhuset Rudbeckia i Rosendal, Uppsala. ", 6)
	got := string(Calendar("", []Event{ev}, stamped))
	for _, line := range strings.Split(got, "\r\n") {
		if len(line) > 75 {
			t.Errorf("a line is %d octets long: %q", len(line), line)
		}
	}
	unfolded := strings.ReplaceAll(got, "\r\n ", "")
	if !strings.Contains(unfolded, "DESCRIPTION:"+strings.ReplaceAll(ev.Description, ",", `\,`)) {
		t.Errorf("unfolding did not give the text back:\n%s", unfolded)
	}
}

// The two web calendars get the same evening as a link, since that is what
// people expect from a button.
func TestTheWebCalendarLinksCarryTheEvening(t *testing.T) {
	ev := evening()
	google := GoogleLink(ev)
	for _, want := range []string{
		"action=TEMPLATE",
		"dates=20260825T160000Z%2F20260825T173000Z",
		"ctz=Europe%2FStockholm",
		"location=stora+matsalen",
	} {
		if !strings.Contains(google, want) {
			t.Errorf("the Google link is missing %q:\n%s", want, google)
		}
	}
	outlook := OutlookLink(ev)
	for _, want := range []string{
		"rru=addevent",
		"startdt=2026-08-25T16%3A00%3A00Z",
		"enddt=2026-08-25T17%3A30%3A00Z",
	} {
		if !strings.Contains(outlook, want) {
			t.Errorf("the Outlook link is missing %q:\n%s", want, outlook)
		}
	}
}

func TestFilenameIsTheEvening(t *testing.T) {
	got := Filename(time.Date(2026, 8, 25, 0, 0, 0, 0, stockholm))
	if got != "middag-2026-08-25.ics" {
		t.Errorf("Filename = %q", got)
	}
}
