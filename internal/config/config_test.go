package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const minimal = `
site:
  title: Test
`

func TestLoadFillsInTheDefaults(t *testing.T) {
	cfg, err := Load(write(t, minimal))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Site.Timezone != "Europe/Stockholm" {
		t.Errorf("Timezone = %q", cfg.Site.Timezone)
	}
	if cfg.Location() == nil {
		t.Fatal("Location is nil")
	}
	wd := cfg.Dinner.ParsedWeekdays()
	if len(wd) != 2 || wd[0] != time.Tuesday || wd[1] != time.Thursday {
		t.Errorf("weekdays = %v, want Tuesday and Thursday", wd)
	}
	if cfg.Dinner.ServingTime != "18:00" {
		t.Errorf("ServingTime = %q", cfg.Dinner.ServingTime)
	}
	// The house's rule: Friday night the week before.
	if cfg.Deadline.ParsedWeekday() != time.Friday {
		t.Errorf("deadline weekday = %v", cfg.Deadline.ParsedWeekday())
	}
	if cfg.Deadline.Minutes() != 23*60+59 {
		t.Errorf("deadline minutes = %d", cfg.Deadline.Minutes())
	}
	if cfg.Deadline.WeeksBefore != 1 {
		t.Errorf("WeeksBefore = %d", cfg.Deadline.WeeksBefore)
	}
}

func TestLoadReadsEverything(t *testing.T) {
	cfg, err := Load(write(t, `
site:
  title: Rudbeckia middagar
  timezone: Europe/Stockholm
dinner:
  weekdays: [måndag, onsdag]
  serving_time: "17:30"
  location: matsalen
deadline:
  weekday: onsdag
  time: "12:00"
  weeks_before: 2
teams:
  - name: Lag 1
    leader: Anna
    email: anna@example.se
season:
  name: Hösten 2026
  start: 2026-08-25
  end: 2026-12-17
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wd := cfg.Dinner.ParsedWeekdays()
	if len(wd) != 2 || wd[0] != time.Monday || wd[1] != time.Wednesday {
		t.Errorf("weekdays = %v", wd)
	}
	if cfg.Deadline.ParsedWeekday() != time.Wednesday || cfg.Deadline.Minutes() != 720 {
		t.Errorf("deadline = %v %d", cfg.Deadline.ParsedWeekday(), cfg.Deadline.Minutes())
	}
	if len(cfg.Teams) != 1 || cfg.Teams[0].Leader != "Anna" {
		t.Errorf("teams = %+v", cfg.Teams)
	}
	if cfg.Season == nil || cfg.Season.Start != "2026-08-25" {
		t.Errorf("season = %+v", cfg.Season)
	}
}

func TestLoadRejectsBadConfigurations(t *testing.T) {
	tests := []struct{ name, body, wants string }{
		{"unknown key", "site:\n  titel: Test\n", "field titel"},
		{"unknown timezone", "site:\n  timezone: Mars/Olympus\n", "unknown timezone"},
		{"bad weekday", "dinner:\n  weekdays: [tisdag, blursdag]\n", "not a weekday"},
		{"repeated weekday", "dinner:\n  weekdays: [tisdag, tis]\n", "twice"},
		{"bad serving time", "dinner:\n  serving_time: \"25:00\"\n", "out of range"},
		{"bad deadline time", "deadline:\n  time: \"halv sex\"\n", "not HH:MM"},
		{"negative weeks", "deadline:\n  weeks_before: -1\n", "cannot be negative"},
		{"nameless team", "teams:\n  - leader: Anna\n", "missing a name"},
		{"bad season date", "season:\n  name: X\n  start: igår\n  end: 2026-12-17\n", "not a date"},
		{"backwards season", "season:\n  name: X\n  start: 2026-12-17\n  end: 2026-08-25\n", "before"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, tc.body))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error %q does not mention %q", err, tc.wants)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected an error for a missing file")
	}
}

func TestParseWeekday(t *testing.T) {
	for _, s := range []string{"tisdag", "TISDAG", " tis ", "2"} {
		got, err := ParseWeekday(s)
		if err != nil || got != time.Tuesday {
			t.Errorf("ParseWeekday(%q) = %v, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "blursdag", "7", "-1"} {
		if _, err := ParseWeekday(s); err == nil {
			t.Errorf("ParseWeekday(%q) should fail", s)
		}
	}
}

func TestClockRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{{"00:00", 0}, {"09:05", 545}, {"23:59", 1439}} {
		got, err := ParseClock(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseClock(%q) = %d, %v", tc.in, got, err)
		}
		if back := FormatClock(got); back != tc.in {
			t.Errorf("FormatClock(%d) = %q, want %q", got, back, tc.in)
		}
	}
	for _, s := range []string{"", "24:00", "12", "12:60", "-1:00", "a:b"} {
		if _, err := ParseClock(s); err == nil {
			t.Errorf("ParseClock(%q) should fail", s)
		}
	}
}

func TestLoadRuntimeNeedsAPassword(t *testing.T) {
	clearEnv(t)
	if _, err := LoadRuntime(); err == nil {
		t.Fatal("expected an error without DINNER_PASSWORD")
	}
	t.Setenv("DINNER_PASSWORD", "hemligt")
	rt, err := LoadRuntime()
	if err != nil {
		t.Fatalf("LoadRuntime: %v", err)
	}
	if rt.ListenAddr != ":8080" || rt.DBPath != "data/dinners.db" {
		t.Errorf("defaults wrong: %+v", rt)
	}
	if len(rt.SessionSecret) != 32 {
		t.Errorf("SessionSecret is %d bytes", len(rt.SessionSecret))
	}
}

// Demo mode has to work with no configuration at all, or the first thing a
// newcomer runs fails.
func TestDemoModeFillsInPasswords(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEMO", "true")
	rt, err := LoadRuntime()
	if err != nil {
		t.Fatalf("LoadRuntime: %v", err)
	}
	if rt.Password != DemoPassword || rt.AdminPassword != DemoAdminPassword {
		t.Errorf("demo passwords = %q/%q", rt.Password, rt.AdminPassword)
	}
}

// Rotating a password should log everyone out; setting SESSION_SECRET should
// stop it doing that.
func TestSessionSecretDerivation(t *testing.T) {
	clearEnv(t)
	t.Setenv("DINNER_PASSWORD", "ett")
	a, _ := LoadRuntime()
	t.Setenv("DINNER_PASSWORD", "tva")
	b, _ := LoadRuntime()
	if string(a.SessionSecret) == string(b.SessionSecret) {
		t.Error("changing the password should change the derived secret")
	}

	t.Setenv("SESSION_SECRET", "stabilt")
	t.Setenv("DINNER_PASSWORD", "tre")
	c, _ := LoadRuntime()
	t.Setenv("DINNER_PASSWORD", "fyra")
	d, _ := LoadRuntime()
	if string(c.SessionSecret) != string(d.SessionSecret) {
		t.Error("an explicit SESSION_SECRET should survive a password change")
	}
}

func TestLoadRuntimeRejectsUnknownEncryption(t *testing.T) {
	clearEnv(t)
	t.Setenv("DINNER_PASSWORD", "hemligt")
	t.Setenv("SMTP_ENCRYPTION", "rot13")
	if _, err := LoadRuntime(); err == nil {
		t.Error("expected an error for an unknown SMTP_ENCRYPTION")
	}
}

func TestBaseURLLosesItsTrailingSlash(t *testing.T) {
	clearEnv(t)
	t.Setenv("DINNER_PASSWORD", "hemligt")
	t.Setenv("BASE_URL", "https://mat.rudbeckia.nu/")
	rt, _ := LoadRuntime()
	if rt.BaseURL != "https://mat.rudbeckia.nu" {
		t.Errorf("BaseURL = %q", rt.BaseURL)
	}
}

func TestMailSettingsEnabled(t *testing.T) {
	if (MailSettings{}).Enabled() {
		t.Error("no host, no mail")
	}
	if (MailSettings{Host: "smtp.example.se"}).Enabled() {
		t.Error("a host without a from address cannot send")
	}
	if !(MailSettings{Host: "smtp.example.se", From: "mat@example.se"}).Enabled() {
		t.Error("host and from should be enough")
	}
}

// clearEnv unsets everything LoadRuntime reads, so a stray variable in the
// developer's shell cannot change what the tests see.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DEMO", "LISTEN_ADDR", "CONFIG_PATH", "DB_PATH", "BASE_URL",
		"DINNER_PASSWORD", "ADMIN_PASSWORD", "SESSION_SECRET", "SESSION_DAYS",
		"TRUST_PROXY", "SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASSWORD",
		"SMTP_FROM", "SMTP_FROM_NAME", "SMTP_ENCRYPTION", "SMTP_REPLY_TO", "SMTP_BCC",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// Every link that leaves the site is built from BASE_URL, so a deployment that
// never set it needs telling rather than quietly mailing out links that point
// at the server's own machine.
func TestBaseURLUnsetIsDetected(t *testing.T) {
	clearEnv(t)
	t.Setenv("DINNER_PASSWORD", "hemligt")
	rt, err := LoadRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if rt.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want the default", rt.BaseURL)
	}
	if !rt.BaseURLUnset() {
		t.Error("an unset BASE_URL should be reported as such")
	}

	t.Setenv("BASE_URL", "https://dinner.rudbeckia.nu")
	rt, _ = LoadRuntime()
	if rt.BaseURLUnset() {
		t.Error("a real address should not be reported as unset")
	}
	// A trailing slash is the same address, not a different one.
	t.Setenv("BASE_URL", DefaultBaseURL+"/")
	rt, _ = LoadRuntime()
	if !rt.BaseURLUnset() {
		t.Error("the default with a trailing slash is still the default")
	}
}
