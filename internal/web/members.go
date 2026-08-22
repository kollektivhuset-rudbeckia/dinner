package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/O5ten/dinners/internal/mattermost"
)

// memberCacheTTL is how long the directory listing is reused. The admin view
// asks for the whole house whenever the teams page is opened, and the house
// does not gain a member between two of those.
const memberCacheTTL = 5 * time.Minute

// memberSuggestion is one person the admin view can pick as a team leader.
// Name is what the administrator reads; Username is what the form submits.
type memberSuggestion struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

// memberList is everyone the house's Mattermost knows about. Truncated says
// the server is too large to send at once, so the list on the page is a part
// of it and a username still has to be allowed to be typed by hand.
type memberList struct {
	Users     []memberSuggestion `json:"users"`
	Truncated bool               `json:"truncated"`
}

// memberCache holds the directory between requests.
type memberCache struct {
	mu   sync.Mutex
	list memberList
	at   time.Time
}

// errNoSuchLeader says nobody in the house answers to what was typed.
var errNoSuchLeader = errors.New("no such mattermost account")

// ambiguousLeader says several people do. It carries them, so the page can
// name them instead of only saying no.
type ambiguousLeader struct{ Candidates []mattermost.User }

func (e ambiguousLeader) Error() string {
	return "several mattermost accounts match: " + describe(e.Candidates)
}

// Who lists the matching people the way the admin view talks about them.
func (e ambiguousLeader) Who() string { return describe(e.Candidates) }

// handleAdminMembers answers the teams page's lookup of who is in the house,
// so the leader field can offer the actual accounts instead of leaving the
// administrator to remember usernames. It is behind the admin gate like the
// page that uses it.
func (s *Server) handleAdminMembers(w http.ResponseWriter, r *http.Request, v *view) {
	out, err := s.memberDirectory(r.Context())
	if err != nil {
		s.log.Error("mattermost directory", "err", err)
		http.Error(w, `{"error":"mattermost"}`, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		s.log.Error("encode member list", "err", err)
	}
}

// memberDirectory returns everyone in the house's Mattermost, from a
// short-lived cache. Without a chat server there is nobody to offer, which
// leaves the leader field a plain text box.
func (s *Server) memberDirectory(ctx context.Context) (memberList, error) {
	s.members.mu.Lock()
	defer s.members.mu.Unlock()
	if !s.members.at.IsZero() && s.now().Sub(s.members.at) < memberCacheTTL {
		return s.members.list, nil
	}

	users, truncated, err := s.mm.Directory(ctx)
	if err != nil {
		return memberList{Users: []memberSuggestion{}}, err
	}
	if truncated {
		s.log.Warn("the mattermost directory is larger than the list the page holds; "+
			"a username can still be typed in full",
			"listed", len(users), "limit", mattermost.DirectoryLimit)
	}

	list := memberList{Users: suggestions(users), Truncated: truncated}
	s.members.list, s.members.at = list, s.now()
	return list, nil
}

// suggestions sorts the accounts by the name the administrator reads, so the
// list is already in a sensible order before anybody types anything.
func suggestions(users []mattermost.User) []memberSuggestion {
	out := make([]memberSuggestion, 0, len(users))
	for _, u := range users {
		if !u.Active() {
			continue
		}
		out = append(out, memberSuggestion{Username: u.Username, Name: u.DisplayName()})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := mattermost.Fold(out[i].Name), mattermost.Fold(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].Username < out[j].Username
	})
	return out
}

// resolveLeader turns what the teams form posted into the account the list
// will be sent to.
//
// An empty field is allowed and means the team has nobody to tell yet — the
// schedule shows that as an evening with no leader rather than refusing to
// save the team. Anything else has to be a real, active person: a team leader
// who is a typo is worse than one who is missing, because the site would go on
// claiming the list had been sent.
//
// The field takes what an administrator is likely to have: a username picked
// from the list, "@anna.andersson" pasted from a message, or a full name,
// because that is what they know. A name that points at exactly one person is
// that person; one that points at several is refused, since guessing which
// neighbour cooks is not ours to do.
func (s *Server) resolveLeader(ctx context.Context, typed string) (mattermost.User, error) {
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return mattermost.User{}, nil
	}
	// Without a chat server there is nothing to look anything up in, so the
	// field is taken as typed. This is what local development does.
	if !s.mm.Enabled() {
		return mattermost.User{Username: mattermost.Username(typed)}, nil
	}
	if u, err := s.mm.ByUsername(ctx, typed); err == nil {
		return u, nil
	}
	// Not a username, then. Perhaps it is a name.
	hits, err := s.mm.Search(ctx, typed)
	if err != nil {
		return mattermost.User{}, err
	}
	// A term that is somebody's whole name or username beats one that merely
	// starts it, so "Anna Andersson" finds Anna even with an Anna Anderssen
	// in the house.
	if exact := exactly(hits, typed); len(exact) > 0 {
		hits = exact
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return mattermost.User{}, errNoSuchLeader
	default:
		return mattermost.User{}, ambiguousLeader{Candidates: hits}
	}
}

// exactly returns the accounts whose full name, nickname or username is the
// term, give or take capitals and accents.
func exactly(users []mattermost.User, term string) []mattermost.User {
	want := mattermost.Fold(term)
	var out []mattermost.User
	for _, u := range users {
		switch want {
		case mattermost.Fold(u.DisplayName()), mattermost.Fold(u.Nickname), mattermost.Fold(u.Username):
			out = append(out, u)
		}
	}
	return out
}

// describe lists people the way the admin view talks about them, so an
// ambiguous name reads as a choice rather than a rejection.
func describe(users []mattermost.User) string {
	const most = 5
	var names []string
	for i, u := range users {
		if i == most {
			names = append(names, "…")
			break
		}
		names = append(names, u.DisplayName()+" (@"+u.Username+")")
	}
	return strings.Join(names, ", ")
}
