package web

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/mail"
)

// notifyKind labels the one mail this site sends, so the log can grow other
// kinds later without the old rows becoming ambiguous.
const notifyKind = "deadline"

// noTeam is recorded as the recipient when a deadline passed with nobody to
// mail. It keeps the notifier from retrying every few minutes for the rest of
// the week, and shows up in the admin schedule as something to fix.
const noTeam = "-"

// StartNotifier runs the deadline mailing in the background until ctx is
// cancelled. Registration closing is the trigger: at that moment the numbers
// are final, and the cooking team is told where to find them.
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

// runNotifications mails the list for every dinner whose registration has just
// closed and which nobody has been told about yet.
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
			s.log.Error("notifier: read mail log", "date", d.Key, "err", err)
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

// sendList mails one evening's link to its cooking-team leader and records
// that it went out. Recording happens first: sending the same list twice is a
// nuisance, and a mail server that is briefly down is better handled by the
// administrator's "send again" button than by a retry loop.
func (s *Server) sendList(ctx context.Context, d dinner.Dinner) error {
	if d.Team == nil || d.Team.LeaderEmail == "" {
		s.log.Warn("no cooking team to mail", "date", d.Key)
		return s.store.MarkNotified(ctx, d.Key, notifyKind, noTeam, s.now())
	}
	if err := s.store.MarkNotified(ctx, d.Key, notifyKind, d.Team.LeaderEmail, s.now()); err != nil {
		return fmt.Errorf("record notification: %w", err)
	}

	// The mail goes to whoever leads the team, and we have no way of knowing
	// which language their browser is set to — so it follows the deployment's
	// own language, the one the house chose.
	lang := s.defaultLang()
	loc := s.cfg.Location()
	when := i18n.DateLong(lang, d.Date.In(loc))
	link := s.listURL(d)
	greeting := firstName(d.Team.LeaderName)
	if greeting == "" {
		greeting = d.Team.Name
	}
	served := i18n.T(lang, "mail.served", i18n.Clock(d.Serving))
	if s.cfg.Dinner.Location != "" {
		served = i18n.T(lang, "mail.served.in", i18n.Clock(d.Serving), s.cfg.Dinner.Location)
	}

	// The mail deliberately carries no names, numbers or diets. Those live on
	// the list, behind the link: one place to look, always current, and
	// nothing sensitive sitting in a mailbox.
	var text bytes.Buffer
	fmt.Fprintf(&text, "%s\n\n", i18n.T(lang, "mail.greeting", greeting))
	fmt.Fprintf(&text, "%s\n\n", i18n.T(lang, "mail.closed", when))
	fmt.Fprintf(&text, "  %s\n\n", link)
	fmt.Fprintf(&text, "%s\n\n", i18n.T(lang, "mail.whatsthere"))
	fmt.Fprintf(&text, "%s\n\n", served)
	fmt.Fprintf(&text, "%s\n%s\n", i18n.T(lang, "mail.regards"), s.cfg.Site.Title)

	var body bytes.Buffer
	fmt.Fprintf(&body, `<p>%s</p>
<p>%s</p>
<p><a href="%s" style="display:inline-block;background:#ad8301;color:#fffcf0;padding:10px 18px;border-radius:6px;text-decoration:none">%s</a></p>
<p>%s</p>`,
		html.EscapeString(i18n.T(lang, "mail.greeting", greeting)),
		html.EscapeString(i18n.T(lang, "mail.closed", when)),
		link,
		html.EscapeString(i18n.T(lang, "mail.open")),
		html.EscapeString(i18n.T(lang, "mail.whatsthere")))
	fmt.Fprintf(&body, `<p style="color:#6f6e69">%s</p>`, html.EscapeString(served))
	fmt.Fprintf(&body, `<p style="color:#6f6e69;font-size:13px">%s</p>`,
		html.EscapeString(s.cfg.Site.Title))

	msg := mail.Message{
		To:      []string{d.Team.LeaderEmail},
		Subject: i18n.T(lang, "mail.subject", when),
		Text:    text.String(),
		HTML:    wrapHTML(s.cfg.Site.Title, body.String()),
	}
	if err := s.mailer.Send(msg); err != nil {
		return fmt.Errorf("send list for %s: %w", d.Key, err)
	}
	s.log.Info("list mailed", "date", d.Key, "team", d.Team.Name, "to", d.Team.LeaderEmail)
	return nil
}

// wrapHTML puts the message body in a plain, mail-client-safe shell.
func wrapHTML(title, body string) string {
	return `<!doctype html><html lang="sv"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>` + html.EscapeString(title) + `</title></head>
<body style="margin:0;padding:24px;background:#fffcf0;color:#100f0f;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;line-height:1.6">
<div style="max-width:560px;margin:0 auto">` + body + `</div></body></html>`
}

func firstName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.IndexByte(name, ' '); i > 0 {
		return name[:i]
	}
	return name
}
