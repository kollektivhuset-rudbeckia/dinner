package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O5ten/dinners/internal/mattermost"
)

// fakeMattermost stands in for the house's chat server: it answers the handful
// of API calls the dinner site makes, and records the direct messages the bot
// sends so tests can read what a cooking-team leader would.
type fakeMattermost struct {
	*httptest.Server

	mu    sync.Mutex
	users map[string]mattermost.User // by username
	dms   []sentDM
	calls map[string]int // API path -> number of requests
}

// sentDM is one direct message the bot delivered.
type sentDM struct {
	Username string
	Message  string
}

const fakeBotID = "bot0000000000000000000000"

// fakeDirectory is the cast the tests cook for. The addresses use the reserved
// example domains, and the ids are as opaque as the real ones.
var fakeDirectory = []mattermost.User{
	{ID: "u-anna", Username: "anna.andersson", FirstName: "Anna", LastName: "Andersson", Email: "anna@example.se"},
	{ID: "u-bo", Username: "bo.bengtsson", FirstName: "Bo", LastName: "Bengtsson", Email: "bo@example.se"},
	{ID: "u-cecilia", Username: "cecilia.dahl", FirstName: "Cecilia", LastName: "Dahl", Email: "cecilia@example.se"},
	{ID: "u-mikael", Username: "mikael.ostberg", FirstName: "Mikael", LastName: "Östberg", Email: "mikael@example.se"},
	// A second Anna Andersson: two people can share a name, and the form has
	// to say so rather than guess which of them cooks.
	{ID: "u-anna2", Username: "anna.a", FirstName: "Anna", LastName: "Andersson", Email: "anna.a@example.se"},
	{ID: "u-inactive", Username: "gammal.granne", FirstName: "Gammal", LastName: "Granne", DeleteAt: 1},
}

func newFakeMattermost(t *testing.T) *fakeMattermost {
	t.Helper()
	f := &fakeMattermost{users: map[string]mattermost.User{}, calls: map[string]int{}}
	for _, u := range fakeDirectory {
		f.users[u.Username] = u
	}

	mux := http.NewServeMux()

	// Every request is counted, so a test can show that the directory is
	// fetched once rather than on every page view.
	count := func(path string, h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.calls[path]++
			f.mu.Unlock()
			h(w, r)
		}
	}

	mux.HandleFunc("GET /api/v4/users", count("users", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("active") != "true" {
			t.Errorf("the directory listing should ask for active accounts only: %s", r.URL)
		}
		// One page is plenty for a house; the client stops when a page comes
		// back short, which every page here does.
		var active []mattermost.User
		if r.URL.Query().Get("page") == "0" {
			for _, u := range fakeDirectory {
				if u.Active() {
					active = append(active, u)
				}
			}
		}
		writeJSON(w, active)
	}))

	mux.HandleFunc("GET /api/v4/users/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, mattermost.User{ID: fakeBotID, Username: "dinner", IsBot: true})
	})

	mux.HandleFunc("GET /api/v4/users/username/{name}", count("username", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		u, ok := f.users[r.PathValue("name")]
		f.mu.Unlock()
		if !ok {
			http.Error(w, `{"message":"Unable to find the user."}`, http.StatusNotFound)
			return
		}
		writeJSON(w, u)
	}))

	mux.HandleFunc("POST /api/v4/users/search", count("search", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Term string `json:"term"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		term := strings.ToLower(strings.TrimSpace(body.Term))
		var hits []mattermost.User
		f.mu.Lock()
		for _, u := range fakeDirectory {
			haystack := strings.ToLower(u.Username + " " + u.FirstName + " " + u.LastName)
			if term != "" && strings.Contains(haystack, term) {
				hits = append(hits, f.users[u.Username])
			}
		}
		f.mu.Unlock()
		writeJSON(w, hits)
	}))

	// A direct channel is named after the person, which is all the fake needs
	// to attribute the posts that follow.
	mux.HandleFunc("POST /api/v4/channels/direct", func(w http.ResponseWriter, r *http.Request) {
		var ids []string
		json.NewDecoder(r.Body).Decode(&ids)
		target := ""
		for _, id := range ids {
			if id != fakeBotID {
				target = id
			}
		}
		writeJSON(w, map[string]string{"id": "dm-" + target})
	})

	mux.HandleFunc("POST /api/v4/posts", count("posts", func(w http.ResponseWriter, r *http.Request) {
		var post struct {
			ChannelID string `json:"channel_id"`
			Message   string `json:"message"`
		}
		json.NewDecoder(r.Body).Decode(&post)
		userID := strings.TrimPrefix(post.ChannelID, "dm-")
		dm := sentDM{Message: post.Message}
		f.mu.Lock()
		for _, u := range fakeDirectory {
			if u.ID == userID {
				dm.Username = u.Username
			}
		}
		f.dms = append(f.dms, dm)
		f.mu.Unlock()
		writeJSON(w, map[string]string{"id": "post-1"})
	}))

	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// requests returns how many times an API path was called.
func (f *fakeMattermost) requests(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

// messages returns the direct messages sent so far.
func (f *fakeMattermost) messages() []sentDM {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentDM{}, f.dms...)
}

// waitForDM waits for the n:th direct message. The notifier runs on its own
// schedule, so there is nothing to synchronise on but the result.
func (f *fakeMattermost) waitForDM(t *testing.T, n int) sentDM {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := f.messages(); len(got) >= n {
			return got[n-1]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no direct message number %d arrived; got %d", n, len(f.messages()))
	return sentDM{}
}
