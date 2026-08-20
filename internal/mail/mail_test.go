package mail

import (
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/O5ten/dinners/internal/config"
)

func sender() *Sender {
	return NewSender(config.MailSettings{
		Host: "smtp.example.se", Port: 587, From: "middag@rudbeckia.nu",
		FromName: "Rudbeckia middagar", Encryption: "starttls",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func message() Message {
	return Message{
		To:      []string{"anna@example.se"},
		Subject: "Matlista: tisdag 25 augusti — specialkost ingår",
		Text:    "Hej Anna!\n\nMatlistan är klar.\n",
		HTML:    "<p>Hej Anna!</p><p>Matlistan är klar.</p>",
		Attachments: []Attachment{{
			Filename:    "matlista.txt",
			ContentType: "text/plain; charset=utf-8",
			Data:        []byte("Vuxna 12\r\nBarn 6\r\n"),
		}},
	}
}

// The message must parse as real MIME, or clients will show it as garbage.
func TestBuildProducesParseableMIME(t *testing.T) {
	raw, err := sender().build(message())
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}

	if got := msg.Header.Get("To"); got != "anna@example.se" {
		t.Errorf("To = %q", got)
	}
	// A non-ASCII subject must be encoded, then decode back to the original.
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("decode subject: %v", err)
	}
	if subject != message().Subject {
		t.Errorf("Subject decoded to %q, want %q", subject, message().Subject)
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("top level type = %q, want multipart/mixed", mediaType)
	}

	var sawAlternative, sawAttachment bool
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		mt, sub, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("part content type: %v", err)
		}
		switch {
		case mt == "multipart/alternative":
			sawAlternative = true
			checkAlternative(t, part, sub["boundary"])
		case mt == "text/plain":
			sawAttachment = true
			if got := part.Header.Get("Content-Disposition"); !strings.Contains(got, "matlista.txt") {
				t.Errorf("attachment disposition = %q", got)
			}
			body, _ := io.ReadAll(part)
			if !strings.Contains(decodeBase64(t, string(body)), "Vuxna 12") {
				t.Error("attachment body did not survive encoding")
			}
		}
	}
	if !sawAlternative {
		t.Error("no multipart/alternative part")
	}
	if !sawAttachment {
		t.Error("no attachment part")
	}
}

func checkAlternative(t *testing.T, part io.Reader, boundary string) {
	t.Helper()
	mr := multipart.NewReader(part, boundary)
	var types []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("alternative part: %v", err)
		}
		mt, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		types = append(types, mt)
		body, _ := io.ReadAll(p)
		decoded := decodeBase64(t, string(body))
		if !strings.Contains(decoded, "Hej Anna!") {
			t.Errorf("%s part lost its content: %q", mt, decoded)
		}
		if !strings.Contains(decoded, "är klar") {
			t.Errorf("%s part lost its non-ASCII characters", mt)
		}
	}
	// Plain text must come first so clients that prefer it pick it up.
	if len(types) != 2 || types[0] != "text/plain" || types[1] != "text/html" {
		t.Errorf("alternative parts = %v, want text/plain then text/html", types)
	}
}

func decodeBase64(t *testing.T, s string) string {
	t.Helper()
	var b strings.Builder
	for _, line := range strings.Split(s, "\r\n") {
		b.WriteString(strings.TrimSpace(line))
	}
	out, err := base64Decode(b.String())
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	return string(out)
}

func TestBase64LinesRespectTheMIMELimit(t *testing.T) {
	raw, err := sender().build(message())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line exceeds the SMTP limit (%d)", len(line))
		}
	}
}

func TestSendWithoutSMTPLogsInsteadOfFailing(t *testing.T) {
	s := NewSender(config.MailSettings{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if s.Enabled() {
		t.Error("Enabled() should be false without a host")
	}
	if err := s.Send(message()); err != nil {
		t.Errorf("Send without SMTP should be a no-op, got %v", err)
	}
}

func TestEnabledNeedsHostAndFrom(t *testing.T) {
	cases := []struct {
		cfg  config.MailSettings
		want bool
	}{
		{config.MailSettings{}, false},
		{config.MailSettings{Host: "smtp.example.se"}, false},
		{config.MailSettings{From: "a@b.se"}, false},
		{config.MailSettings{Host: "smtp.example.se", From: "a@b.se"}, true},
	}
	for _, c := range cases {
		if got := c.cfg.Enabled(); got != c.want {
			t.Errorf("Enabled(%+v) = %v, want %v", c.cfg, got, c.want)
		}
	}
}

// An unreachable mail server must fail quickly instead of pinning the goroutine.
func TestSendGivesUpOnAnUnreachableServer(t *testing.T) {
	s := NewSender(config.MailSettings{
		// 203.0.113.0/24 is reserved for documentation and never routes.
		Host: "203.0.113.1", Port: 587, From: "middag@rudbeckia.nu",
		Encryption: "none",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- s.Send(message()) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a dial failure")
		}
		if elapsed := time.Since(start); elapsed > dialTimeout+5*time.Second {
			t.Errorf("took %v to give up, want roughly %v", elapsed, dialTimeout)
		}
	case <-time.After(dialTimeout + 10*time.Second):
		t.Fatal("Send hung past its dial timeout")
	}
}
