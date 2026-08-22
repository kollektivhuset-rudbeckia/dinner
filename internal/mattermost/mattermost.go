// Package mattermost talks to the house's Mattermost server as the dinner bot.
// It does two things: look up accounts in the user directory, so a cooking
// team's leader can be named by their username, and send that leader a direct
// message when registration closes.
//
// When no server or token is configured the client is disabled: lookups
// answer with the name as typed and messages are written to the log instead of
// being sent. That keeps local development and the demo working without a chat
// server.
package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// timeout bounds a single API call. The bot is on the same network as the
// site, so anything slower than this is broken rather than busy.
const timeout = 15 * time.Second

// searchLimit caps how many people a directory search returns at once.
const searchLimit = 20

const (
	// directoryPage is how many accounts one listing request asks for. 200 is
	// the largest page Mattermost serves.
	directoryPage = 200
	// DirectoryLimit caps a full directory listing. A house has tens of
	// members, not thousands; the cap is a guard against paging through a
	// large company server forever, and callers are told when it bites.
	DirectoryLimit = 1000
)

// User is the part of a Mattermost account the dinner site cares about.
type User struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Nickname  string `json:"nickname"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"email"`
	IsBot     bool   `json:"is_bot"`
	DeleteAt  int64  `json:"delete_at"`
}

// Active reports whether the account is a real, non-deactivated person.
func (u User) Active() bool { return u.ID != "" && u.DeleteAt == 0 && !u.IsBot }

// DisplayName is the name to show in the house's own words: real name when the
// account has one, otherwise the nickname, otherwise the username.
func (u User) DisplayName() string {
	full := strings.TrimSpace(u.FirstName + " " + u.LastName)
	switch {
	case full != "":
		return full
	case u.Nickname != "":
		return u.Nickname
	default:
		return u.Username
	}
}

// Client is the bot's connection to Mattermost.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	log     *slog.Logger

	// self is the bot's own account, needed to open a direct channel. It is
	// filled by Verify at startup.
	self User
}

// New builds a client. An empty url or token yields a disabled client.
func New(baseURL, token string, log *slog.Logger) *Client {
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: timeout},
		log:     log,
	}
}

// Enabled reports whether calls will really reach a Mattermost server.
func (c *Client) Enabled() bool { return c.baseURL != "" && c.token != "" }

// BaseURL is the server address, for links back into Mattermost.
func (c *Client) BaseURL() string { return c.baseURL }

// Bot returns the bot's own account, once Verify has run.
func (c *Client) Bot() User { return c.self }

// Verify checks the token and remembers who the bot is. Callers should run it
// at startup so a bad token is a loud failure rather than a silent one later.
func (c *Client) Verify(ctx context.Context) (User, error) {
	if !c.Enabled() {
		return User{}, nil
	}
	var u User
	if err := c.call(ctx, http.MethodGet, "/api/v4/users/me", nil, &u); err != nil {
		return User{}, err
	}
	c.self = u
	return u, nil
}

// Search finds people in the directory, for the admin view's leader lookup. A
// disabled client finds nobody, which leaves the plain text field working.
func (c *Client) Search(ctx context.Context, term string) ([]User, error) {
	term = strings.TrimSpace(term)
	if !c.Enabled() || term == "" {
		return nil, nil
	}
	body := map[string]any{
		"term":           term,
		"allow_inactive": false,
		"limit":          searchLimit,
	}
	var users []User
	if err := c.call(ctx, http.MethodPost, "/api/v4/users/search", body, &users); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(users))
	for _, u := range users {
		if u.Active() {
			out = append(out, u)
		}
	}
	return out, nil
}

// Directory lists the people who could lead a cooking team, so the admin form
// can offer them all without a round trip per keystroke. The bool reports that
// the listing was cut short at DirectoryLimit, which means the caller is
// looking at part of a much larger server.
func (c *Client) Directory(ctx context.Context) ([]User, bool, error) {
	if !c.Enabled() {
		return nil, false, nil
	}
	var out []User
	for page := 0; ; page++ {
		var batch []User
		path := fmt.Sprintf("/api/v4/users?active=true&per_page=%d&page=%d", directoryPage, page)
		if err := c.call(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, false, err
		}
		for _, u := range batch {
			if u.Active() {
				out = append(out, u)
			}
		}
		if len(batch) < directoryPage {
			return out, false, nil
		}
		if len(out) >= DirectoryLimit {
			return out[:DirectoryLimit], true, nil
		}
	}
}

// ByUsername looks up one account. A disabled client answers with the name as
// typed and no id, so a team can still name its leader without a chat server.
func (c *Client) ByUsername(ctx context.Context, username string) (User, error) {
	username = normalizeUsername(username)
	if username == "" {
		return User{}, fmt.Errorf("empty username")
	}
	if !c.Enabled() {
		return User{Username: username}, nil
	}
	var u User
	path := "/api/v4/users/username/" + url.PathEscape(username)
	if err := c.call(ctx, http.MethodGet, path, nil, &u); err != nil {
		return User{}, err
	}
	if !u.Active() {
		return User{}, fmt.Errorf("mattermost user %q is not an active person", username)
	}
	return u, nil
}

// DM sends a direct message from the bot to one user. A disabled client logs
// the message instead, so nothing is lost silently.
func (c *Client) DM(ctx context.Context, userID, message string) error {
	if !c.Enabled() {
		c.log.Warn("mattermost not configured, direct message not sent", "user", userID)
		c.log.Debug("mattermost message body", "user", userID, "message", message)
		return nil
	}
	if userID == "" {
		return fmt.Errorf("no mattermost user id to send to")
	}
	if c.self.ID == "" {
		if _, err := c.Verify(ctx); err != nil {
			return fmt.Errorf("identify bot: %w", err)
		}
	}

	// A direct channel is created on demand and reused afterwards, so asking
	// for it every time is both correct and cheap.
	var channel struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, "/api/v4/channels/direct",
		[]string{c.self.ID, userID}, &channel); err != nil {
		return fmt.Errorf("open direct channel: %w", err)
	}
	return c.call(ctx, http.MethodPost, "/api/v4/posts",
		map[string]any{"channel_id": channel.ID, "message": message}, nil)
}

// call performs one API request, encoding body and decoding into out when
// they are non-nil.
func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mattermost %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if out == nil {
		// Drain so the connection can be reused.
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// apiError turns Mattermost's error body into something readable in a log.
func apiError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var e struct {
		Message string `json:"message"`
		ID      string `json:"id"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		return fmt.Errorf("mattermost %s: %s", resp.Status, e.Message)
	}
	return fmt.Errorf("mattermost %s", resp.Status)
}

// folder reduces the accented letters that turn up in the house's names to
// their plain forms. It is deliberately a short table rather than full Unicode
// normalization: these are the letters people actually type differently.
var folder = strings.NewReplacer(
	"å", "a", "ä", "a", "á", "a", "à", "a", "â", "a", "ã", "a",
	"ö", "o", "ø", "o", "ó", "o", "ò", "o", "ô", "o", "õ", "o",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ý", "y", "ÿ", "y", "ñ", "n", "ç", "c", "ð", "d", "þ", "th", "ß", "ss",
	"æ", "ae", "œ", "oe", "ł", "l", "š", "s", "ž", "z", "č", "c", "ř", "r",
)

// Fold turns a name into the form searches compare: lowercase, without
// accents, so "Östberg" and "ostberg" find each other.
func Fold(s string) string {
	return folder.Replace(strings.ToLower(strings.TrimSpace(s)))
}

// normalizeUsername accepts what an administrator is likely to type: "@anna",
// "Anna" or a pasted profile link all become "anna".
func normalizeUsername(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimPrefix(s, "@")
	return strings.ToLower(strings.TrimSpace(s))
}

// Username is the exported spelling of normalizeUsername, for callers that
// clean up form input before storing it.
func Username(s string) string { return normalizeUsername(s) }
