// Package store persists everything the dinner registration needs in SQLite:
// the cooking teams, the seasons, the schedule's exceptions, the standing and
// one-off registrations, and a log of which lists have already been sent.
//
// Dinner dates are stored as plain "2006-01-02" strings in the house's own
// timezone. A dinner is a calendar evening, not an instant, and keeping it a
// date avoids every daylight-saving trap. Timestamps, which really are
// instants, are stored as UTC RFC3339.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Kind separates a household in the house from a visitor who registered on the
// public page.
type Kind string

const (
	KindMember Kind = "member"
	KindGuest  Kind = "guest"
)

// Diet is what a registration eats. One choice covers everyone in it: a
// household that needs two different meals makes two registrations rather than
// splitting one, which keeps the cooking team's totals a simple sum.
type Diet string

const (
	DietOmnivore    Diet = "allatare"
	DietFlexitarian Diet = "flexitarian"
	DietPescetarian Diet = "pescetarian"
	DietVegetarian  Diet = "vegetarian"
	DietVegan       Diet = "vegan"
)

// Diets are the choices in the order they are offered and counted, from least
// to most restrictive. The order is deliberate: it is also the order of the
// columns on the printed matlista.
var Diets = []Diet{
	DietOmnivore, DietFlexitarian, DietPescetarian, DietVegetarian, DietVegan,
}

// ParseDiet reads a stored or submitted value, falling back to eating
// everything so a missing or unknown choice can never lose a portion.
func ParseDiet(s string) (Diet, bool) {
	for _, d := range Diets {
		if string(d) == s {
			return d, true
		}
	}
	return DietOmnivore, false
}

// Valid reports whether d is one of the offered diets.
func (d Diet) Valid() bool {
	_, ok := ParseDiet(string(d))
	return ok
}

// Registration is one household's answer for one dinner. Adults and Children
// count everyone eating, and Diet is what all of them are served.
//
// A registration with nobody in it is not an absence of an answer: it is the
// answer "we are not coming", which is what lets a household opt out of a
// dinner their standing registration would otherwise cover.
type Registration struct {
	ID   string
	Date string
	Kind Kind
	// Member is the household's Mattermost username, lowercased and without
	// the @. It is what one household's answers are found by; a guest has none.
	Member string
	// MMUserID is the immutable Mattermost account id, which is how the bot
	// reaches the household with a confirmation.
	MMUserID  string
	Name      string
	Apartment string
	Host      string
	Adults    int
	Children  int
	Diet      Diet
	Note      string
	Token     string
	CreatedAt time.Time
	UpdatedAt time.Time
	CreatedIP string
}

// People is how many will eat.
func (r Registration) People() int { return r.Adults + r.Children }

// Attending reports whether anyone is coming at all.
func (r Registration) Attending() bool { return r.People() > 0 }

// Guest reports whether this came in through the public page.
func (r Registration) Guest() bool { return r.Kind == KindGuest }

// Standing is a household's default answer for every dinner on one weekday —
// the replacement for the permanent-registration sheet. A registration for a
// specific date always wins over it.
type Standing struct {
	ID string
	// Member is the household's Mattermost username, as on a registration.
	Member    string
	MMUserID  string
	Weekday   time.Weekday
	Name      string
	Apartment string
	Adults    int
	Children  int
	Diet      Diet
	Note      string
	UpdatedAt time.Time
}

// Team is a cooking team. The leader is who receives the direct message with
// the totals and the link to the list.
type Team struct {
	ID   int64
	Name string
	// LeaderName is how the house spells the leader's name. Nobody types it:
	// it is copied from their Mattermost account when the team is saved, so
	// that the schedule and the list can name them without asking the chat
	// server on every page view.
	LeaderName string
	// LeaderUsername is the leader's Mattermost username, lowercased and
	// without the @. It is the only way the site can reach a person, so a team
	// without one is a team nobody hears from.
	LeaderUsername string
	Position       int
	Active         bool
}

// Label is the team's name with its leader, for the schedule.
func (t Team) Label() string {
	if t.LeaderName == "" {
		return t.Name
	}
	return t.Name + " · " + t.LeaderName
}

// Season is a stretch of the year when dinners are cooked, and which evenings
// of the week they land on.
type Season struct {
	ID       int64
	Name     string
	Start    string
	End      string
	Weekdays []time.Weekday
	// RotationOffset shifts which team takes the season's first dinner, so a
	// new season can carry on where the last one stopped.
	RotationOffset int
}

// Break is a stretch with no dinners at all: a school holiday, the weeks
// around Christmas, a public holiday. The evenings inside it are never
// generated, which means the cooking rotation pauses rather than letting the
// teams whose turn it was lose it.
type Break struct {
	ID    int64
	Name  string
	Start string
	End   string
}

// Covers reports whether a "2006-01-02" date falls inside the break.
func (b Break) Covers(date string) bool { return date >= b.Start && date <= b.End }

// Overlaps reports whether two seasons cover any of the same days. Two seasons
// that overlap would disagree about which evenings are dinners and which team
// cooks them, so the admin view refuses to store one.
func (s Season) Overlaps(other Season) bool {
	return s.Start <= other.End && other.Start <= s.End
}

// Override is an administrator's edit to one evening of the generated
// schedule: a cancelled dinner, another team than the rotation picked, or a
// note shown to the house.
type Override struct {
	Date      string
	Cancelled bool
	TeamID    sql.NullInt64
	Note      string
	UpdatedAt time.Time
}

// Settings are the values the administrator changes from the admin view. They
// are seeded from config.yaml the first time the database is created.
type Settings struct {
	// DeadlineWeekday, DeadlineMinutes and DeadlineWeeksBefore place the
	// weekly registration deadline; see config.Deadline for what they mean.
	DeadlineWeekday     time.Weekday
	DeadlineMinutes     int
	DeadlineWeeksBefore int
	// GuestOpen turns the public /gast page on and off.
	GuestOpen bool
}

// Store is the persistence handle.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS teams (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	name            TEXT NOT NULL,
	leader_name     TEXT NOT NULL DEFAULT '',
	leader_username TEXT NOT NULL DEFAULT '',
	position        INTEGER NOT NULL DEFAULT 0,
	active          INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS seasons (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	name            TEXT NOT NULL,
	start_date      TEXT NOT NULL,
	end_date        TEXT NOT NULL,
	weekdays        TEXT NOT NULL DEFAULT '2,4',
	rotation_offset INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS breaks (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	start_date TEXT NOT NULL,
	end_date   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS overrides (
	date       TEXT PRIMARY KEY,
	cancelled  INTEGER NOT NULL DEFAULT 0,
	team_id    INTEGER,
	note       TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS registrations (
	id          TEXT PRIMARY KEY,
	date        TEXT NOT NULL,
	kind        TEXT NOT NULL DEFAULT 'member',
	member      TEXT NOT NULL DEFAULT '',
	mm_user_id  TEXT NOT NULL DEFAULT '',
	name        TEXT NOT NULL,
	apartment   TEXT NOT NULL DEFAULT '',
	host        TEXT NOT NULL DEFAULT '',
	adults      INTEGER NOT NULL DEFAULT 0,
	children    INTEGER NOT NULL DEFAULT 0,
	diet        TEXT NOT NULL DEFAULT 'allatare',
	note        TEXT NOT NULL DEFAULT '',
	token       TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL,
	created_ip  TEXT NOT NULL DEFAULT ''
);
-- One answer per household per dinner. Guests are not covered: they have no
-- account here at all and are found by their own token instead.
CREATE UNIQUE INDEX IF NOT EXISTS idx_reg_member
	ON registrations (date, member) WHERE kind = 'member' AND member <> '';
CREATE INDEX IF NOT EXISTS idx_reg_date ON registrations (date);
CREATE INDEX IF NOT EXISTS idx_reg_household ON registrations (member, date);
CREATE INDEX IF NOT EXISTS idx_reg_token ON registrations (token);

CREATE TABLE IF NOT EXISTS standing (
	id          TEXT PRIMARY KEY,
	member      TEXT NOT NULL,
	mm_user_id  TEXT NOT NULL DEFAULT '',
	weekday     INTEGER NOT NULL,
	name        TEXT NOT NULL,
	apartment   TEXT NOT NULL DEFAULT '',
	adults      INTEGER NOT NULL DEFAULT 0,
	children    INTEGER NOT NULL DEFAULT 0,
	diet        TEXT NOT NULL DEFAULT 'allatare',
	note        TEXT NOT NULL DEFAULT '',
	updated_at  TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_standing ON standing (member, weekday);

-- Which lists have been sent, so a restart cannot send the same one twice.
CREATE TABLE IF NOT EXISTS notifications (
	date      TEXT NOT NULL,
	kind      TEXT NOT NULL,
	recipient TEXT NOT NULL,
	sent_at   TEXT NOT NULL,
	PRIMARY KEY (date, kind)
);
`

// Open opens (and if needed creates) the database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	// _txlock=immediate makes every transaction take the write lock up front.
	// Without it two concurrent writers can both start as readers and then
	// collide when they try to upgrade, which SQLite reports as
	// SQLITE_BUSY_SNAPSHOT and busy_timeout cannot retry away.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer at a time; a small pool avoids lock churn.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	// The columns and indexes an older database has to lose before the schema
	// can be applied at all, then the schema, then the rest of the upgrades.
	if err := prepare(db); err != nil {
		return nil, fmt.Errorf("prepare database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return &Store{db: db}, nil
}

// prepare makes an older database fit for the current schema. It runs before
// the schema is applied, because the indexes the schema creates name columns
// that a database from an earlier version does not have yet.
//
// Households used to be identified by an e-mail address and are now identified
// by their Mattermost account. One cannot be turned into the other — an
// address is not a username — so the switch cannot be a backfill:
//
//   - The address itself goes. Nothing can be sent to it any more, so keeping
//     it would only leave stale personal data in the table.
//   - Standing registrations are removed. One with no household behind it
//     would go on adding people to every dinner with nobody able to change it
//     or withdraw it.
//   - Registrations for dinners still to come go the same way, so that a
//     household registering again is not counted twice. Evenings already
//     served keep their rows: they are the house's history, and the cooking
//     team's list for them stays exactly as it was.
func prepare(db *sql.DB) error {
	has, err := hasColumn(db, "registrations", "email")
	if err != nil {
		return err
	}
	if has {
		// The indexes go first: SQLite refuses to drop a column an index names.
		for _, index := range []string{"idx_reg_member", "idx_reg_email", "idx_standing"} {
			if _, err := db.Exec(`DROP INDEX IF EXISTS ` + index); err != nil {
				return fmt.Errorf("drop %s: %w", index, err)
			}
		}
		if _, err := db.Exec(
			`DELETE FROM registrations WHERE kind = 'member' AND date >= date('now')`); err != nil {
			return fmt.Errorf("clear the registrations with no household: %w", err)
		}
		if _, err := db.Exec(`DELETE FROM standing`); err != nil {
			return fmt.Errorf("clear the standing registrations: %w", err)
		}
		for _, table := range []string{"registrations", "standing"} {
			gone, err := hasColumn(db, table, "email")
			if err != nil {
				return err
			}
			if !gone {
				continue
			}
			if _, err := db.Exec(`ALTER TABLE ` + table + ` DROP COLUMN email`); err != nil {
				return fmt.Errorf("drop %s.email: %w", table, err)
			}
		}
	}

	// The columns that replace the address: the household's account, and the
	// id the bot needs to reach it with a confirmation. Both are added only to
	// tables that already exist; a new database gets them from the schema.
	for _, table := range []string{"registrations", "standing"} {
		exists, err := hasTable(db, table)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		for _, column := range []string{"member", "mm_user_id"} {
			has, err := hasColumn(db, table, column)
			if err != nil {
				return err
			}
			if has {
				continue
			}
			if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column +
				` TEXT NOT NULL DEFAULT ''`); err != nil {
				return fmt.Errorf("add %s.%s: %w", table, column, err)
			}
		}
	}
	return nil
}

// migrate brings a database written by an older build up to date. There is no
// version table: each step asks the schema what it looks like and does nothing
// if it is already right, which is enough for a single deployment and cannot
// get out of step with itself.
func migrate(db *sql.DB) error {
	// Diets used to be two counts per registration — how many vegans and how
	// many vegetarians — and are now one choice for the whole registration.
	// Anything that was partly vegan or vegetarian becomes that diet outright,
	// since that is the meal the cooking team has to produce.
	// The list used to be mailed to the team leader and is now sent to them in
	// Mattermost. The address is not merely unused after the switch — nothing
	// can be sent to it any more — so it goes rather than sitting in the table
	// as stale personal data.
	if has, err := hasColumn(db, "teams", "leader_username"); err != nil {
		return err
	} else if !has {
		if _, err := db.Exec(
			`ALTER TABLE teams ADD COLUMN leader_username TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add teams.leader_username: %w", err)
		}
	}
	if has, err := hasColumn(db, "teams", "leader_email"); err != nil {
		return err
	} else if has {
		if _, err := db.Exec(`ALTER TABLE teams DROP COLUMN leader_email`); err != nil {
			return fmt.Errorf("drop teams.leader_email: %w", err)
		}
	}

	for _, table := range []string{"registrations", "standing"} {
		has, err := hasColumn(db, table, "diet")
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + table +
			` ADD COLUMN diet TEXT NOT NULL DEFAULT 'allatare'`); err != nil {
			return fmt.Errorf("add %s.diet: %w", table, err)
		}
		old, err := hasColumn(db, table, "vegans")
		if err != nil {
			return err
		}
		if !old {
			continue
		}
		if _, err := db.Exec(`UPDATE ` + table + ` SET diet = CASE
			WHEN vegans > 0      THEN 'vegan'
			WHEN vegetarians > 0 THEN 'vegetarian'
			ELSE 'allatare' END`); err != nil {
			return fmt.Errorf("backfill %s.diet: %w", table, err)
		}
	}
	return nil
}

func hasTable(db *sql.DB, table string) (bool, error) {
	rows, err := db.Query(
		`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`SELECT 1 FROM pragma_table_info(?) WHERE name = ?`, table, column)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("hittades inte")

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}

// ---------------------------------------------------------------- settings --

// Settings reads the administrator-managed settings, falling back to def for
// anything not stored yet.
func (s *Store) Settings(ctx context.Context, def Settings) (Settings, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return def, err
	}
	defer rows.Close()
	out := def
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return def, err
		}
		switch k {
		case "deadline_weekday":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 6 {
				out.DeadlineWeekday = time.Weekday(n)
			}
		case "deadline_minutes":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < 24*60 {
				out.DeadlineMinutes = n
			}
		case "deadline_weeks_before":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				out.DeadlineWeeksBefore = n
			}
		case "guest_open":
			out.GuestOpen = v == "1"
		}
	}
	return out, rows.Err()
}

// SaveSettings writes every setting in one transaction.
func (s *Store) SaveSettings(ctx context.Context, st Settings) error {
	pairs := [][2]string{
		{"deadline_weekday", strconv.Itoa(int(st.DeadlineWeekday))},
		{"deadline_minutes", strconv.Itoa(st.DeadlineMinutes)},
		{"deadline_weeks_before", strconv.Itoa(st.DeadlineWeeksBefore)},
		{"guest_open", boolStr(st.GuestOpen)},
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range pairs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, p[0], p[1]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SettingsStored reports whether the settings row set has been written at all,
// which is how first-run seeding knows there is nothing to preserve.
func (s *Store) SettingsStored(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings`).Scan(&n)
	return n > 0, err
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// ------------------------------------------------------------------- teams --

// Teams returns every team, in rotation order.
func (s *Store) Teams(ctx context.Context) ([]Team, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, leader_name, leader_username, position, active
		 FROM teams ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.LeaderName, &t.LeaderUsername, &t.Position, &t.Active); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ActiveTeams returns the teams that take part in the rotation.
func (s *Store) ActiveTeams(ctx context.Context) ([]Team, error) {
	all, err := s.Teams(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, t := range all {
		if t.Active {
			out = append(out, t)
		}
	}
	return out, nil
}

// SaveTeam inserts or updates a team. An ID of zero inserts, and the new team
// goes last in the rotation.
//
// Position is deliberately not taken from the caller: the order is a
// permutation, and the only way to change it is ReorderTeams, which writes the
// whole sequence at once. That makes two teams sharing a place impossible
// rather than merely unlikely.
func (s *Store) SaveTeam(ctx context.Context, t Team) (int64, error) {
	if t.ID == 0 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		var next int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(position) + 1, 0) FROM teams`).Scan(&next); err != nil {
			return 0, err
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO teams (name, leader_name, leader_username, position, active)
			 VALUES (?,?,?,?,?)`,
			t.Name, t.LeaderName, t.LeaderUsername, next, t.Active)
		if err != nil {
			return 0, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		return id, tx.Commit()
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE teams SET name=?, leader_name=?, leader_username=?, active=? WHERE id=?`,
		t.Name, t.LeaderName, t.LeaderUsername, t.Active, t.ID)
	return t.ID, err
}

// ReorderTeams writes the rotation order in one go. Whatever the caller sends,
// the result is always 0, 1, 2 … with no gaps and no ties: ids that do not
// exist are ignored, each id counts once, and any team the caller forgot keeps
// its relative place at the end.
func (s *Store) ReorderTeams(ctx context.Context, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id FROM teams ORDER BY position, id`)
	if err != nil {
		return err
	}
	var existing []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		existing = append(existing, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	known := make(map[int64]bool, len(existing))
	for _, id := range existing {
		known[id] = true
	}
	placed := make(map[int64]bool, len(existing))
	order := make([]int64, 0, len(existing))
	for _, id := range ids {
		if known[id] && !placed[id] {
			placed[id] = true
			order = append(order, id)
		}
	}
	for _, id := range existing {
		if !placed[id] {
			order = append(order, id)
		}
	}

	for i, id := range order {
		if _, err := tx.ExecContext(ctx,
			`UPDATE teams SET position = ? WHERE id = ?`, i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteTeam removes a team and detaches it from any evening that pointed at
// it, which drops those evenings back onto the plain rotation.
func (s *Store) DeleteTeam(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE overrides SET team_id = NULL WHERE team_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM teams WHERE id = ?`, id); err != nil {
		return err
	}
	// Close the gap the deletion left, so the order stays 0, 1, 2 …
	if _, err := tx.ExecContext(ctx, `
		UPDATE teams SET position = (
			SELECT COUNT(*) FROM teams AS earlier
			WHERE earlier.position < teams.position
			   OR (earlier.position = teams.position AND earlier.id < teams.id)
		)`); err != nil {
		return err
	}
	return tx.Commit()
}

// ----------------------------------------------------------------- seasons --

// Seasons returns every season, earliest first.
func (s *Store) Seasons(ctx context.Context) ([]Season, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, start_date, end_date, weekdays, rotation_offset
		 FROM seasons ORDER BY start_date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Season
	for rows.Next() {
		var se Season
		var wd string
		if err := rows.Scan(&se.ID, &se.Name, &se.Start, &se.End, &wd, &se.RotationOffset); err != nil {
			return nil, err
		}
		se.Weekdays = ParseWeekdayList(wd)
		out = append(out, se)
	}
	return out, rows.Err()
}

// SaveSeason inserts or updates a season. An ID of zero inserts.
func (s *Store) SaveSeason(ctx context.Context, se Season) (int64, error) {
	wd := FormatWeekdayList(se.Weekdays)
	if se.ID == 0 {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO seasons (name, start_date, end_date, weekdays, rotation_offset)
			 VALUES (?,?,?,?,?)`, se.Name, se.Start, se.End, wd, se.RotationOffset)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE seasons SET name=?, start_date=?, end_date=?, weekdays=?, rotation_offset=?
		 WHERE id=?`, se.Name, se.Start, se.End, wd, se.RotationOffset, se.ID)
	return se.ID, err
}

// DeleteSeason removes a season. Registrations for its dates are left alone;
// they simply stop being reachable until a season covers them again.
func (s *Store) DeleteSeason(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM seasons WHERE id = ?`, id)
	return err
}

// ParseWeekdayList reads "2,4" into weekdays.
func ParseWeekdayList(s string) []time.Weekday {
	var out []time.Weekday
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 || n > 6 {
			continue
		}
		out = append(out, time.Weekday(n))
	}
	return out
}

// FormatWeekdayList renders weekdays as "2,4".
func FormatWeekdayList(wd []time.Weekday) string {
	parts := make([]string, 0, len(wd))
	for _, d := range wd {
		parts = append(parts, strconv.Itoa(int(d)))
	}
	return strings.Join(parts, ",")
}

// ------------------------------------------------------------------ breaks --

// Breaks returns every break, earliest first.
func (s *Store) Breaks(ctx context.Context) ([]Break, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, start_date, end_date FROM breaks ORDER BY start_date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Break
	for rows.Next() {
		var b Break
		if err := rows.Scan(&b.ID, &b.Name, &b.Start, &b.End); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SaveBreak inserts or updates a break. An ID of zero inserts.
func (s *Store) SaveBreak(ctx context.Context, b Break) (int64, error) {
	if b.ID == 0 {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO breaks (name, start_date, end_date) VALUES (?,?,?)`,
			b.Name, b.Start, b.End)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE breaks SET name=?, start_date=?, end_date=? WHERE id=?`,
		b.Name, b.Start, b.End, b.ID)
	return b.ID, err
}

// DeleteBreak removes a break, which brings its evenings back.
func (s *Store) DeleteBreak(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM breaks WHERE id = ?`, id)
	return err
}

// --------------------------------------------------------------- overrides --

// Overrides returns the administrator's edits for dates in [from, to].
func (s *Store) Overrides(ctx context.Context, from, to string) (map[string]Override, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT date, cancelled, team_id, note, updated_at FROM overrides
		 WHERE date >= ? AND date <= ?`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Override{}
	for rows.Next() {
		var o Override
		var updated string
		if err := rows.Scan(&o.Date, &o.Cancelled, &o.TeamID, &o.Note, &updated); err != nil {
			return nil, err
		}
		if o.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, err
		}
		out[o.Date] = o
	}
	return out, rows.Err()
}

// SaveOverride writes one evening's exception, or removes it when it says
// nothing: no cancellation, no team and no note is the same as no row.
func (s *Store) SaveOverride(ctx context.Context, o Override) error {
	if !o.Cancelled && !o.TeamID.Valid && strings.TrimSpace(o.Note) == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM overrides WHERE date = ?`, o.Date)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO overrides (date, cancelled, team_id, note, updated_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT(date) DO UPDATE SET
		   cancelled = excluded.cancelled,
		   team_id   = excluded.team_id,
		   note      = excluded.note,
		   updated_at = excluded.updated_at`,
		o.Date, o.Cancelled, o.TeamID, strings.TrimSpace(o.Note), utc(o.UpdatedAt))
	return err
}

// ---------------------------------------------------------- registrations --

const regCols = `id, date, kind, member, mm_user_id, name, apartment, host,
	adults, children, diet, note, token, created_at, updated_at, created_ip`

func scanReg(row interface{ Scan(...any) error }) (Registration, error) {
	var r Registration
	var created, updated string
	err := row.Scan(&r.ID, &r.Date, &r.Kind, &r.Member, &r.MMUserID, &r.Name,
		&r.Apartment, &r.Host, &r.Adults, &r.Children, &r.Diet, &r.Note, &r.Token,
		&created, &updated, &r.CreatedIP)
	if err != nil {
		return r, err
	}
	if r.CreatedAt, err = parseTime(created); err != nil {
		return r, err
	}
	r.UpdatedAt, err = parseTime(updated)
	return r, err
}

func (s *Store) queryRegs(ctx context.Context, q string, args ...any) ([]Registration, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Registration
	for rows.Next() {
		r, err := scanReg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Registrations returns every registration for one dinner, members first and
// then guests, alphabetically within each.
func (s *Store) Registrations(ctx context.Context, date string) ([]Registration, error) {
	return s.queryRegs(ctx, `SELECT `+regCols+` FROM registrations
		WHERE date = ? ORDER BY kind, lower(name)`, date)
}

// RegistrationsBetween returns registrations for dates in [from, to].
func (s *Store) RegistrationsBetween(ctx context.Context, from, to string) ([]Registration, error) {
	return s.queryRegs(ctx, `SELECT `+regCols+` FROM registrations
		WHERE date >= ? AND date <= ? ORDER BY date, kind, lower(name)`, from, to)
}

// Member normalizes a Mattermost username into the form used as the household
// key: lowercase, no leading @.
func Member(username string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
}

// MemberRegistrations returns one household's own registrations from a date on.
func (s *Store) MemberRegistrations(ctx context.Context, member, from string) ([]Registration, error) {
	return s.queryRegs(ctx, `SELECT `+regCols+` FROM registrations
		WHERE kind = 'member' AND member = ? AND date >= ? ORDER BY date`,
		Member(member), from)
}

// MemberRegistration returns one household's answer for one dinner.
func (s *Store) MemberRegistration(ctx context.Context, date, member string) (Registration, error) {
	if Member(member) == "" {
		return Registration{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+regCols+` FROM registrations
		WHERE date = ? AND member = ? AND kind = 'member'`, date, Member(member))
	r, err := scanReg(row)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// RegistrationByToken finds a guest's own registration from their link.
func (s *Store) RegistrationByToken(ctx context.Context, token string) (Registration, error) {
	if token == "" {
		return Registration{}, ErrNotFound
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+regCols+` FROM registrations WHERE token = ?`, token)
	r, err := scanReg(row)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// SaveRegistration inserts a registration, or replaces the household's earlier
// answer for the same dinner. Guests are always inserted; they are identified
// by their token, not by an account.
func (s *Store) SaveRegistration(ctx context.Context, r Registration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if r.Kind == KindMember {
		r.Member = Member(r.Member)
		var id, token string
		var created string
		err := tx.QueryRowContext(ctx,
			`SELECT id, token, created_at FROM registrations
			 WHERE date = ? AND member = ? AND kind = 'member'`, r.Date, r.Member).
			Scan(&id, &token, &created)
		switch {
		case err == nil:
			// Keep the row's identity and its original timestamp, so an edit
			// does not look like a brand new registration.
			r.ID, r.Token = id, token
			if t, err := parseTime(created); err == nil {
				r.CreatedAt = t
			}
		case errors.Is(err, sql.ErrNoRows):
		default:
			return err
		}
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO registrations (`+regCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   mm_user_id=excluded.mm_user_id,
		   name=excluded.name, apartment=excluded.apartment, host=excluded.host,
		   adults=excluded.adults, children=excluded.children,
		   diet=excluded.diet, note=excluded.note, updated_at=excluded.updated_at`,
		r.ID, r.Date, r.Kind, r.Member, r.MMUserID, r.Name, r.Apartment, r.Host,
		r.Adults, r.Children, r.Diet, r.Note, r.Token,
		utc(r.CreatedAt), utc(r.UpdatedAt), r.CreatedIP)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteRegistration removes one registration outright.
func (s *Store) DeleteRegistration(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM registrations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// -------------------------------------------------------------- standing --

const standingCols = `id, member, mm_user_id, weekday, name, apartment,
	adults, children, diet, note, updated_at`

func scanStanding(row interface{ Scan(...any) error }) (Standing, error) {
	var st Standing
	var wd int
	var updated string
	err := row.Scan(&st.ID, &st.Member, &st.MMUserID, &wd, &st.Name, &st.Apartment,
		&st.Adults, &st.Children, &st.Diet, &st.Note, &updated)
	if err != nil {
		return st, err
	}
	st.Weekday = time.Weekday(wd)
	st.UpdatedAt, err = parseTime(updated)
	return st, err
}

func (s *Store) queryStanding(ctx context.Context, q string, args ...any) ([]Standing, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Standing
	for rows.Next() {
		st, err := scanStanding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// StandingFor returns every household's standing registration for one weekday.
func (s *Store) StandingFor(ctx context.Context, wd time.Weekday) ([]Standing, error) {
	return s.queryStanding(ctx, `SELECT `+standingCols+` FROM standing
		WHERE weekday = ? ORDER BY lower(name)`, int(wd))
}

// StandingByMember returns one household's standing registrations.
func (s *Store) StandingByMember(ctx context.Context, member string) ([]Standing, error) {
	if Member(member) == "" {
		return nil, nil
	}
	return s.queryStanding(ctx, `SELECT `+standingCols+` FROM standing
		WHERE member = ? ORDER BY weekday`, Member(member))
}

// AllStanding returns every standing registration, for the admin view.
func (s *Store) AllStanding(ctx context.Context) ([]Standing, error) {
	return s.queryStanding(ctx, `SELECT `+standingCols+` FROM standing
		ORDER BY lower(name), weekday`)
}

// SaveStanding writes a household's default for one weekday. A standing
// registration with nobody in it is meaningless, so it is deleted instead.
func (s *Store) SaveStanding(ctx context.Context, st Standing) error {
	st.Member = Member(st.Member)
	if st.Member == "" {
		return fmt.Errorf("a standing registration needs a household")
	}
	if st.Adults+st.Children <= 0 {
		return s.DeleteStanding(ctx, st.Member, st.Weekday)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO standing (`+standingCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(member, weekday) DO UPDATE SET
		   mm_user_id=excluded.mm_user_id,
		   name=excluded.name, apartment=excluded.apartment,
		   adults=excluded.adults, children=excluded.children,
		   diet=excluded.diet, note=excluded.note, updated_at=excluded.updated_at`,
		st.ID, st.Member, st.MMUserID, int(st.Weekday), st.Name, st.Apartment,
		st.Adults, st.Children, st.Diet, st.Note, utc(st.UpdatedAt))
	return err
}

// DeleteStanding removes a household's default for one weekday.
func (s *Store) DeleteStanding(ctx context.Context, member string, wd time.Weekday) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM standing WHERE member = ? AND weekday = ?`, Member(member), int(wd))
	return err
}

// ---------------------------------------------------------- notifications --

// Notified reports whether a message of this kind has already gone out for a
// date.
func (s *Store) Notified(ctx context.Context, date, kind string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM notifications WHERE date = ? AND kind = ?`, date, kind).Scan(&n)
	return n > 0, err
}

// MarkNotified records a sent message. It fails if one was already recorded,
// which is what keeps two server instances from both sending.
func (s *Store) MarkNotified(ctx context.Context, date, kind, recipient string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO notifications (date, kind, recipient, sent_at) VALUES (?,?,?,?)`,
		date, kind, recipient, utc(at))
	return err
}

// ClearNotified forgets that a message was sent, so the administrator can ask for
// it again.
func (s *Store) ClearNotified(ctx context.Context, date, kind string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM notifications WHERE date = ? AND kind = ?`, date, kind)
	return err
}

// Notification is one line of the notification log.
type Notification struct {
	Date string
	// Recipient is the Mattermost username the list was sent to.
	Recipient string
	SentAt    time.Time
}

// SentNotifications returns the notification log for dates in [from, to], by
// date.
func (s *Store) SentNotifications(ctx context.Context, from, to string) (map[string]Notification, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT date, recipient, sent_at FROM notifications
		 WHERE date >= ? AND date <= ?`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Notification{}
	for rows.Next() {
		var n Notification
		var at string
		if err := rows.Scan(&n.Date, &n.Recipient, &at); err != nil {
			return nil, err
		}
		if n.SentAt, err = parseTime(at); err != nil {
			return nil, err
		}
		out[n.Date] = n
	}
	return out, rows.Err()
}

// Empty reports whether the database has never been used, which is how demo
// seeding avoids piling example data on top of real registrations.
func (s *Store) Empty(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM registrations) + (SELECT COUNT(*) FROM seasons)
		        + (SELECT COUNT(*) FROM teams)`).Scan(&n)
	return n == 0, err
}
