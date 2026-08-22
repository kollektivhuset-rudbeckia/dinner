// Package config loads the site's static description from YAML, plus the
// runtime settings that come from the environment.
//
// Everything that the cooking teams change during a season — teams, season
// dates, the registration deadline — lives in the database and is edited in
// the admin view. The YAML file holds what is fixed for a deployment, and the
// values used to seed an empty database the very first time it is opened.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole YAML file.
type Config struct {
	Site     Site     `yaml:"site"`
	Dinner   Dinner   `yaml:"dinner"`
	Deadline Deadline `yaml:"deadline"`
	Teams    []Team   `yaml:"teams"`
	Season   *Season  `yaml:"season"`

	location *time.Location
}

// Site holds presentation-level settings shared by every page.
type Site struct {
	Title string `yaml:"title"`
	// Language is what a visitor sees before choosing for themselves, and the
	// language the cooking team's message is written in. "sv" or "en".
	Language   string `yaml:"language"`
	Tagline    string `yaml:"tagline"`
	HouseName  string `yaml:"house_name"`
	Timezone   string `yaml:"timezone"`
	HomeURL    string `yaml:"home_url"`
	SupportURL string `yaml:"support_url"`
	FooterNote string `yaml:"footer_note"`
}

// Dinner describes the meal itself: which evenings, when it is served and
// where. The weekdays here are the default for a new season; a season may
// override them.
type Dinner struct {
	Weekdays    []string `yaml:"weekdays"`
	ServingTime string   `yaml:"serving_time"`
	Location    string   `yaml:"location"`
	// GuestInfo is the house's own words at the top of the public guest page,
	// in place of the built-in ones. Give both languages if you set it at all:
	// a page that switches to English and keeps one Swedish paragraph looks
	// broken. Either may be left out, in which case the other is used, and if
	// both are empty the built-in phrase is shown instead.
	GuestInfo   string `yaml:"guest_info"`
	GuestInfoEN string `yaml:"guest_info_en"`

	weekdays []time.Weekday
}

// GuestText returns the house's own wording for a language, or an empty string
// when it has nothing to say and the built-in phrase should be used.
func (d Dinner) GuestText(lang string) string {
	sv, en := strings.TrimSpace(d.GuestInfo), strings.TrimSpace(d.GuestInfoEN)
	if lang == "en" {
		if en != "" {
			return en
		}
		return sv
	}
	if sv != "" {
		return sv
	}
	return en
}

// ParsedWeekdays returns the dinner evenings as time.Weekday values.
func (d Dinner) ParsedWeekdays() []time.Weekday { return d.weekdays }

// Deadline says when registration closes for a whole dinner week.
//
// The deadline is weekly rather than per dinner: the cooking team needs the
// numbers before it shops, and both of the week's dinners are shopped for at
// once. WeeksBefore counts back whole weeks from the Monday of the dinner's
// week, and Weekday/Time then pick the moment inside that earlier week.
//
// The default — Friday 23:59, one week before — means everything served in a
// week must be registered by the night into Saturday nine days earlier.
type Deadline struct {
	Weekday     string `yaml:"weekday"`
	Time        string `yaml:"time"`
	WeeksBefore int    `yaml:"weeks_before"`

	weekday time.Weekday
	minutes int
}

// ParsedWeekday returns the deadline's weekday.
func (d Deadline) ParsedWeekday() time.Weekday { return d.weekday }

// Minutes returns the deadline time as minutes since midnight.
func (d Deadline) Minutes() int { return d.minutes }

// Team is a cooking team and the leader who receives the list. Teams in the
// YAML only seed an empty database; after that the admin view owns them.
//
// The leader is named by their Mattermost username and nothing else: their
// name is on their account, and a second one written here could only drift
// from it.
type Team struct {
	Name string `yaml:"name"`
	// Mattermost is the leader's username in the house's chat, which is where
	// the list is sent when registration closes. Written without the @.
	Mattermost string `yaml:"mattermost"`
}

// Season is the stretch of the year when dinners are cooked. Like Teams it is
// only a seed value for an empty database.
type Season struct {
	Name  string `yaml:"name"`
	Start string `yaml:"start"`
	End   string `yaml:"end"`
}

// Runtime holds the settings that come from the environment rather than YAML,
// because they are secrets or deployment specific.
type Runtime struct {
	ListenAddr    string
	ConfigPath    string
	DBPath        string
	BaseURL       string
	Password      string
	AdminPassword string
	SessionSecret []byte
	SessionMaxAge time.Duration
	Mattermost    MattermostSettings
	TrustProxy    bool
	// Demo fills in throwaway passwords, seeds a season with example
	// registrations and shows a banner saying so. Never enable it for real.
	Demo bool
}

// BaseURLUnset reports whether BASE_URL was left at its default. Every link
// that leaves the site — the message to the cooking team, the address a
// spreadsheet fetches, the one to give a guest — is built from it, so this is
// worth saying out loud rather than discovering from a dead link.
func (r Runtime) BaseURLUnset() bool { return r.BaseURL == DefaultBaseURL }

// MattermostSettings configures the bot that sends the list to the cooking
// team's leader and answers the admin view's lookups of who is in the house.
type MattermostSettings struct {
	// URL is the Mattermost server, e.g. "https://chat.rudbeckia.nu".
	URL string
	// Token is the bot account's personal access token.
	Token string
}

// Enabled reports whether the bot can really reach Mattermost.
func (m MattermostSettings) Enabled() bool { return m.URL != "" && m.Token != "" }

// Load reads and validates the YAML configuration at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) normalize() error {
	if c.Site.Title == "" {
		c.Site.Title = "Middagar"
	}
	if c.Site.Timezone == "" {
		c.Site.Timezone = "Europe/Stockholm"
	}
	if c.Site.Language == "" {
		c.Site.Language = "sv"
	}
	if c.Site.Language != "sv" && c.Site.Language != "en" {
		return fmt.Errorf(`site.language must be "sv" or "en", not %q`, c.Site.Language)
	}
	loc, err := time.LoadLocation(c.Site.Timezone)
	if err != nil {
		return fmt.Errorf("unknown timezone %q: %w", c.Site.Timezone, err)
	}
	c.location = loc

	if len(c.Dinner.Weekdays) == 0 {
		c.Dinner.Weekdays = []string{"tisdag", "torsdag"}
	}
	seen := map[time.Weekday]bool{}
	for _, name := range c.Dinner.Weekdays {
		wd, err := ParseWeekday(name)
		if err != nil {
			return fmt.Errorf("dinner.weekdays: %w", err)
		}
		if seen[wd] {
			return fmt.Errorf("dinner.weekdays lists %s twice", name)
		}
		seen[wd] = true
		c.Dinner.weekdays = append(c.Dinner.weekdays, wd)
	}
	if c.Dinner.ServingTime == "" {
		c.Dinner.ServingTime = "18:00"
	}
	if _, err := ParseClock(c.Dinner.ServingTime); err != nil {
		return fmt.Errorf("dinner.serving_time: %w", err)
	}

	if c.Deadline.Weekday == "" {
		c.Deadline.Weekday = "fredag"
	}
	if c.Deadline.weekday, err = ParseWeekday(c.Deadline.Weekday); err != nil {
		return fmt.Errorf("deadline.weekday: %w", err)
	}
	if c.Deadline.Time == "" {
		c.Deadline.Time = "23:59"
	}
	if c.Deadline.minutes, err = ParseClock(c.Deadline.Time); err != nil {
		return fmt.Errorf("deadline.time: %w", err)
	}
	if c.Deadline.WeeksBefore == 0 {
		c.Deadline.WeeksBefore = 1
	}
	if c.Deadline.WeeksBefore < 0 {
		return fmt.Errorf("deadline.weeks_before cannot be negative")
	}

	for i, t := range c.Teams {
		if strings.TrimSpace(t.Name) == "" {
			return fmt.Errorf("teams[%d] is missing a name", i)
		}
		// A leader is named by their Mattermost username, not by their name in
		// the house: a value with a space in it would be stored as it stands
		// and then match nobody when the list is to be sent.
		if u := strings.TrimSpace(t.Mattermost); strings.ContainsAny(u, " \t") {
			return fmt.Errorf("teams[%d] (%s): mattermost is %q, which is not a username — "+
				"write it the way it appears after the @, for example anna.andersson", i, t.Name, u)
		}
	}
	if c.Season != nil {
		start, err := ParseDate(c.Season.Start, loc)
		if err != nil {
			return fmt.Errorf("season.start: %w", err)
		}
		end, err := ParseDate(c.Season.End, loc)
		if err != nil {
			return fmt.Errorf("season.end: %w", err)
		}
		if end.Before(start) {
			return fmt.Errorf("season.end (%s) is before season.start (%s)", c.Season.End, c.Season.Start)
		}
	}
	return nil
}

// Location is the timezone every date and time is presented in.
func (c *Config) Location() *time.Location { return c.location }

var svWeekdayNames = map[string]time.Weekday{
	"söndag": time.Sunday, "sondag": time.Sunday, "sön": time.Sunday, "son": time.Sunday,
	"måndag": time.Monday, "mandag": time.Monday, "mån": time.Monday, "man": time.Monday,
	"tisdag": time.Tuesday, "tis": time.Tuesday,
	"onsdag": time.Wednesday, "ons": time.Wednesday,
	"torsdag": time.Thursday, "tor": time.Thursday,
	"fredag": time.Friday, "fre": time.Friday,
	"lördag": time.Saturday, "lordag": time.Saturday, "lör": time.Saturday, "lor": time.Saturday,
}

// ParseWeekday accepts a Swedish weekday name, or the number time.Weekday uses.
func ParseWeekday(s string) (time.Weekday, error) {
	key := strings.ToLower(strings.TrimSpace(s))
	if wd, ok := svWeekdayNames[key]; ok {
		return wd, nil
	}
	if n, err := strconv.Atoi(key); err == nil && n >= 0 && n <= 6 {
		return time.Weekday(n), nil
	}
	return 0, fmt.Errorf("%q is not a weekday", s)
}

// ParseClock parses "HH:MM" into minutes since midnight.
func ParseClock(s string) (int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	return h*60 + m, nil
}

// FormatClock renders minutes since midnight as "HH:MM".
func FormatClock(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

// ParseDate reads a "2006-01-02" date as midnight in loc.
func ParseDate(s string, loc *time.Location) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a date (YYYY-MM-DD)", s)
	}
	return t, nil
}
