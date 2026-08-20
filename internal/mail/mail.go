// Package mail sends the notifications to the cooking teams over SMTP. When
// no SMTP host is configured the message is logged instead, which keeps local
// development and first-run deployments working without a mail server.
package mail

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/config"
)

// Attachment is a file carried by the message.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Message is one outgoing e-mail.
type Message struct {
	To          []string
	Subject     string
	Text        string
	HTML        string
	Attachments []Attachment
}

const (
	// dialTimeout bounds how long we wait to reach the mail server.
	dialTimeout = 10 * time.Second
	// sendTimeout bounds the whole conversation once connected.
	sendTimeout = 30 * time.Second
)

// Sender delivers messages.
type Sender struct {
	cfg config.MailSettings
	log *slog.Logger
}

// NewSender builds a Sender from the mail settings.
func NewSender(cfg config.MailSettings, log *slog.Logger) *Sender {
	return &Sender{cfg: cfg, log: log}
}

// Enabled reports whether messages will really be delivered.
func (s *Sender) Enabled() bool { return s.cfg.Enabled() }

// Send delivers a message, or logs it when SMTP is not configured. Sending is
// synchronous; callers that must not block should run it in a goroutine.
func (s *Sender) Send(m Message) error {
	body, err := s.build(m)
	if err != nil {
		return err
	}
	if !s.cfg.Enabled() {
		s.log.Warn("smtp not configured, e-mail not sent",
			"to", strings.Join(m.To, ", "), "subject", m.Subject)
		s.log.Debug("e-mail body", "body", m.Text)
		return nil
	}

	rcpt := append([]string{}, m.To...)
	if s.cfg.BCC != "" {
		rcpt = append(rcpt, s.cfg.BCC)
	}
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprint(s.cfg.Port))

	// Every step gets a deadline. Without one an unreachable mail server would
	// pin the sending goroutine forever.
	dialer := &net.Dialer{Timeout: dialTimeout}
	var conn net.Conn
	if s.cfg.Encryption == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: s.cfg.Host})
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp dial %s: %w", addr, err)
	}
	if err := conn.SetDeadline(time.Now().Add(sendTimeout)); err != nil {
		conn.Close()
		return fmt.Errorf("smtp deadline: %w", err)
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if s.cfg.Encryption == "starttls" {
		if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	for _, to := range rcpt {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("smtp rcpt %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", err)
	}
	return c.Quit()
}

// build renders the MIME message. Layout:
//
//	multipart/mixed
//	├── multipart/alternative (text + html)
//	└── attachments
func (s *Sender) build(m Message) ([]byte, error) {
	var b bytes.Buffer
	boundaryMixed := "mixed-" + boundary()
	boundaryAlt := "alt-" + boundary()

	from := s.cfg.From
	if from == "" {
		from = "middag@localhost"
	}
	fmt.Fprintf(&b, "From: %s <%s>\r\n", mime.QEncoding.Encode("utf-8", s.cfg.FromName), from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(m.To, ", "))
	if s.cfg.ReplyTo != "" {
		fmt.Fprintf(&b, "Reply-To: %s\r\n", s.cfg.ReplyTo)
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Auto-Submitted: auto-generated\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", boundaryMixed)

	fmt.Fprintf(&b, "--%s\r\n", boundaryMixed)
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", boundaryAlt)

	fmt.Fprintf(&b, "--%s\r\n", boundaryAlt)
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&b, "Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(chunk(base64.StdEncoding.EncodeToString([]byte(m.Text))))

	if m.HTML != "" {
		fmt.Fprintf(&b, "--%s\r\n", boundaryAlt)
		fmt.Fprintf(&b, "Content-Type: text/html; charset=utf-8\r\n")
		fmt.Fprintf(&b, "Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(chunk(base64.StdEncoding.EncodeToString([]byte(m.HTML))))
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundaryAlt)

	for _, a := range m.Attachments {
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		fmt.Fprintf(&b, "--%s\r\n", boundaryMixed)
		fmt.Fprintf(&b, "Content-Type: %s; name=\"%s\"\r\n", ct, a.Filename)
		fmt.Fprintf(&b, "Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(&b, "Content-Disposition: attachment; filename=\"%s\"\r\n\r\n", a.Filename)
		b.WriteString(chunk(base64.StdEncoding.EncodeToString(a.Data)))
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundaryMixed)
	return b.Bytes(), nil
}

// chunk breaks base64 into 76-character lines as required by MIME.
func chunk(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76])
		b.WriteString("\r\n")
		s = s[76:]
	}
	b.WriteString(s)
	b.WriteString("\r\n")
	return b.String()
}

func boundary() string {
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
