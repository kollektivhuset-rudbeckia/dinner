package web

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/ical"
	"github.com/O5ten/dinners/internal/mattermost"
	"github.com/O5ten/dinners/internal/store"
)

// notifyKind labels the message sent when registration closes, so the log can
// hold other kinds without the old rows becoming ambiguous.
const notifyKind = "deadline"

// dmTimeout bounds one confirmation. It is generous: the household is already
// looking at its own page, and nobody is waiting for this.
const dmTimeout = 30 * time.Second

// noTeam is recorded as the recipient when a deadline passed with nobody to
// tell. It keeps the notifier from retrying every few minutes for the rest of
// the week, and shows up in the admin schedule as something to fix.
const noTeam = "-"

// ------------------------------------------------------ the confirmation --

// notifyRegistered confirms a household's own answer in the chat, with the
// evening attached as a calendar file. This is what replaced the confirmation
// e-mail: the message lands where the household already reads, and the file
// puts the dinner in whatever calendar they keep.
//
// A registration for nobody is the answer "we are not coming", so it is
// confirmed as that rather than as a booking, and carries a cancellation for
// the calendar in case the evening was already in it.
func (s *Server) notifyRegistered(reg store.Registration, d dinner.Dinner) {
	if reg.MMUserID == "" {
		// A household that said who it was while Mattermost was unconfigured
		// has no account to reach. Say so once rather than pretending the
		// message went out.
		s.log.Warn("no mattermost account on the registration, confirmation not sent",
			"date", reg.Date, "member", reg.Member)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), dmTimeout)
	defer cancel()

	lang := s.memberLang(ctx, reg.Member)
	loc := s.cfg.Location()
	when := i18n.DateLong(lang, d.Date.In(loc))
	ev := s.event(d, lang)
	ev.Cancelled = !reg.Attending()

	var m bytes.Buffer
	if reg.Attending() {
		fmt.Fprintf(&m, "%s\n\n", i18n.T(lang, "dm.registered",
			firstName(reg.Name), when))
		m.WriteString("| | |\n|---|---|\n")
		row := func(label, value string) {
			fmt.Fprintf(&m, "| **%s** | %s |\n", cell(label), cell(value))
		}
		row(i18n.T(lang, "dm.when"), i18n.TitleCase(when)+", "+i18n.Clock(d.Serving))
		if s.cfg.Dinner.Location != "" {
			row(i18n.T(lang, "dm.where"), s.cfg.Dinner.Location)
		}
		row(i18n.T(lang, "dm.people"), headcount(lang, reg))
		row(i18n.T(lang, "dm.diet"), DietLabel(lang, reg.Diet))
		if reg.Note != "" {
			row(i18n.T(lang, "dm.note"), reg.Note)
		}
		m.WriteString("\n")
	} else {
		fmt.Fprintf(&m, "%s\n\n", i18n.T(lang, "dm.declined",
			firstName(reg.Name), when))
	}
	fmt.Fprintf(&m, "%s\n", i18n.T(lang, "dm.change",
		s.rt.BaseURL+"/middag/"+d.Key, i18n.DateTime(lang, d.Closes)))
	if reg.Attending() {
		fmt.Fprintf(&m, "%s\n", i18n.T(lang, "dm.attachment", ical.GoogleLink(ev)))
	}

	file := mattermost.File{
		Filename: ical.Filename(d.Date.In(loc)),
		Data:     ical.Calendar(s.cfg.Site.Title, []ical.Event{ev}, s.now()),
	}
	if err := s.mm.DM(ctx, reg.MMUserID, m.String(), file); err != nil {
		s.log.Error("send confirmation", "date", reg.Date, "member", reg.Member, "err", err)
		return
	}
	s.log.Info("confirmation sent", "date", reg.Date, "member", reg.Member,
		"coming", reg.Attending(), "language", lang)
}

// headcount is the household in words: how many adults and how many children.
func headcount(lang i18n.Lang, reg store.Registration) string {
	switch {
	case reg.Children == 0:
		return i18n.Count(lang, "adult", reg.Adults)
	case reg.Adults == 0:
		return i18n.Count(lang, "child", reg.Children)
	default:
		return i18n.Count(lang, "adult", reg.Adults) + ", " +
			i18n.Count(lang, "child", reg.Children)
	}
}

// memberLang is the language to write to a household in: the one they have set
// in Mattermost, or the deployment's own when they have set nothing we know.
//
// This is not the language of the page the registration was made on. Somebody
// who reads the site in English while their chat is Swedish gets Swedish
// messages, because the message arrives in the chat, on its terms.
func (s *Server) memberLang(ctx context.Context, member string) i18n.Lang {
	if member == "" || !s.mm.Enabled() {
		return s.defaultLang()
	}
	u, err := s.mm.ByUsername(ctx, member)
	if err != nil {
		// Not knowing the setting is not a reason to send nothing; the house's
		// own language is the right guess.
		s.log.Debug("could not read the member's language", "member", member, "err", err)
		return s.defaultLang()
	}
	if lang, ok := i18n.Parse(u.Locale); ok {
		return lang
	}
	return s.defaultLang()
}

// ------------------------------------------------------------- the list --

// StartNotifier runs the deadline notification in the background until ctx is
// cancelled. Registration closing is the trigger: at that moment the numbers
// are final, and the cooking team is told what they add up to and where the
// list is.
func (s *Server) StartNotifier(ctx context.Context, every time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		// Run once at start too, so a server that was down over the deadline
		// catches up as soon as it is back.
		s.runNotifications(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.runNotifications(ctx)
			}
		}
	}()
}

// runNotifications messages the list for every dinner whose registration has
// just closed and which nobody has been told about yet.
func (s *Server) runNotifications(ctx context.Context) {
	world, err := s.world(ctx)
	if err != nil {
		s.log.Error("notifier: load schedule", "err", err)
		return
	}
	now := s.now().In(s.cfg.Location())
	for _, d := range world.Schedule.Dinners {
		if d.Cancelled || d.Over(now) || now.Before(d.Closes) {
			continue
		}
		done, err := s.store.Notified(ctx, d.Key, notifyKind)
		if err != nil {
			s.log.Error("notifier: read the notification log", "date", d.Key, "err", err)
			continue
		}
		if done {
			continue
		}
		if err := s.sendList(ctx, d); err != nil {
			s.log.Error("notifier: send list", "date", d.Key, "err", err)
		}
	}
}

// sendList direct-messages one evening's totals and its link to the cooking
// team's leader, and records that it went out. Recording happens first:
// sending the same list twice is a nuisance, and a chat server that is briefly
// unreachable is better handled by the administrator's "send again" button
// than by a retry loop.
func (s *Server) sendList(ctx context.Context, d dinner.Dinner) error {
	if d.Team == nil || d.Team.LeaderUsername == "" {
		s.log.Warn("no cooking-team leader to message", "date", d.Key)
		return s.store.MarkNotified(ctx, d.Key, notifyKind, noTeam, s.now())
	}
	username := d.Team.LeaderUsername
	if err := s.store.MarkNotified(ctx, d.Key, notifyKind, username, s.now()); err != nil {
		return fmt.Errorf("record notification: %w", err)
	}

	sum, err := s.summary(ctx, d)
	if err != nil {
		return fmt.Errorf("summarize %s: %w", d.Key, err)
	}
	leader, err := s.mm.ByUsername(ctx, username)
	if err != nil {
		return fmt.Errorf("look up @%s: %w", username, err)
	}
	if err := s.mm.DM(ctx, leader.ID, s.listMessage(d, sum)); err != nil {
		return fmt.Errorf("send list for %s: %w", d.Key, err)
	}
	s.log.Info("list sent", "date", d.Key, "team", d.Team.Name, "to", username)
	return nil
}

// listMessage writes what the cooking team's leader reads in Mattermost: the
// totals they shop by, and a link to the list for everything else.
//
// The message goes to whoever leads the team, and we have no way of knowing
// which language their browser is set to — so it follows the deployment's own
// language, the one the house chose.
//
// The numbers are in the message because that is what the leader wants at a
// glance, in the chat they already read. Names and allergies are not: they
// belong to the households who wrote them, and they stay on the list, one
// click away, where they are always current.
func (s *Server) listMessage(d dinner.Dinner, sum dinner.Summary) string {
	lang := s.defaultLang()
	loc := s.cfg.Location()
	when := i18n.DateLong(lang, d.Date.In(loc))
	greeting := firstName(d.Team.LeaderName)
	if greeting == "" {
		greeting = d.Team.Name
	}

	var m bytes.Buffer
	fmt.Fprintf(&m, "%s %s\n\n", i18n.T(lang, "chat.greeting", greeting),
		i18n.T(lang, "chat.closed", when))

	if sum.Empty() {
		fmt.Fprintf(&m, "%s\n\n", i18n.T(lang, "chat.nobody"))
	} else {
		m.WriteString("| | |\n|---|---|\n")
		row := func(label string, n int) {
			fmt.Fprintf(&m, "| **%s** | %s |\n", cell(label), strconv.Itoa(n))
		}
		row(i18n.T(lang, "chat.row.households"), sum.Households)
		row(i18n.T(lang, "chat.row.adults"), sum.Adults)
		row(i18n.T(lang, "chat.row.children"), sum.Children)
		row(i18n.T(lang, "chat.row.portions"), sum.People)
		// Every diet is listed even at zero, exactly as on the printed list: a
		// pot that is not needed this week should read as a nought rather than
		// go missing.
		for _, c := range sum.Diets {
			row(DietLabel(lang, c.Diet), c.People)
		}
		row(i18n.T(lang, "chat.row.guests"), sum.Guests)
		row(i18n.T(lang, "chat.row.allergies"), len(sum.Notes))
		m.WriteString("\n")
	}

	fmt.Fprintf(&m, "[%s](%s)\n\n", i18n.T(lang, "chat.open"), s.listURL(d))
	fmt.Fprintf(&m, "%s\n\n", i18n.T(lang, "chat.whatsthere"))
	if s.cfg.Dinner.Location != "" {
		fmt.Fprintf(&m, "%s\n", i18n.T(lang, "chat.served.in",
			i18n.Clock(d.Serving), s.cfg.Dinner.Location))
	} else {
		fmt.Fprintf(&m, "%s\n", i18n.T(lang, "chat.served", i18n.Clock(d.Serving)))
	}
	return m.String()
}

// cell escapes the pipe characters that would otherwise split a Markdown table
// cell in two. Nothing else in these messages is written by a member.
func cell(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func firstName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.IndexByte(name, ' '); i > 0 {
		return name[:i]
	}
	return name
}
