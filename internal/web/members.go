package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/mattermost"
	"github.com/O5ten/dinners/internal/store"
)

// memberCacheTTL is how long the directory listing is reused. The browser asks
// for the whole house whenever a form with a person in it is opened, and the
// house does not gain a member between two of those.
const memberCacheTTL = 5 * time.Minute

// memberSuggestion is one row in a picker. Name is what the reader sees;
// Username is what the form submits.
type memberSuggestion struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

// memberList is the picker's whole world: everyone in the house's Mattermost.
// Truncated says the server is too large to send at once, so the browser
// should ask this server to search instead of indexing the list itself.
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

// handleMembers answers the pickers' lookups of who is in the house: the
// household saying who it is, and the admin view naming a cooking team's
// leader. Without a query it returns everyone, which the browser indexes and
// searches as you type; with ?q= it searches server-side, which is the
// fallback for a directory too large to send.
//
// It is behind the house password like every other page, so the list of who
// lives here never leaves the house.
func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request, v *view) {
	var (
		out memberList
		err error
	)
	if term := strings.TrimSpace(r.URL.Query().Get("q")); term != "" {
		out, err = s.searchMembers(r.Context(), term)
	} else {
		out, err = s.memberDirectory(r.Context())
	}
	if err != nil {
		s.log.Error("mattermost directory", "err", err)
		http.Error(w, `{"error":"could not read the Mattermost directory"}`, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		s.log.Error("encode member list", "err", err)
	}
}

// searchMembers asks Mattermost to search, for a term of at least two letters.
func (s *Server) searchMembers(ctx context.Context, term string) (memberList, error) {
	out := memberList{Users: []memberSuggestion{}}
	if len([]rune(term)) < 2 {
		return out, nil
	}
	users, err := s.mm.Search(ctx, term)
	if err != nil {
		return out, err
	}
	out.Users = suggestions(users)
	return out, nil
}

// memberDirectory returns everyone in the house's Mattermost, from a
// short-lived cache. Without a chat server there is nobody to offer, which
// leaves the field a plain text box.
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
		s.log.Warn("the mattermost directory is larger than the picker holds; "+
			"falling back to searching on the server",
			"listed", len(users), "limit", mattermost.DirectoryLimit)
	}

	list := memberList{Users: suggestions(users), Truncated: truncated}
	s.members.list, s.members.at = list, s.now()
	return list, nil
}

// suggestions sorts the accounts by the name the reader reads, so an
// unfiltered list is already in a sensible order before anybody types.
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

// findMember turns what someone typed into one account, or into the sentence
// they should read. The field is a plain text input, so it has to cope with
// everything a person might reasonably leave in it: a username picked from the
// list, "@anna.andersson" pasted from a message, a full name, a nickname, or
// just "Anna" because that is all they know. Anything that points at exactly
// one person resolves to that person; anything that points at several says who
// they are, so the next keystroke settles it. Only a term that matches nobody
// is an error.
func (s *Server) findMember(ctx context.Context, lang i18n.Lang, typed string) (mattermost.User, string) {
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return mattermost.User{}, i18n.T(lang, "member.whose")
	}
	// Without a chat server there is nothing to look anything up in, so the
	// field is taken as typed. This is what the demo and local development do.
	if !s.mm.Enabled() {
		return mattermost.User{Username: asUsername(typed)}, ""
	}

	// A username is an exact address: look it up directly and skip searching.
	if username := mattermost.Username(typed); looksLikeUsername(username) {
		if u, err := s.mm.ByUsername(ctx, username); err == nil {
			return u, ""
		}
		// Not a username after all — fall through and search for it as a name,
		// so "Bo" finds Bo even though it looked like one.
	}

	candidates, err := s.mm.Search(ctx, typed)
	if err != nil {
		s.log.Error("mattermost name search", "term", typed, "err", err)
		return mattermost.User{}, i18n.T(lang, "member.unreachable")
	}

	// A term that is somebody's whole name, nickname or username wins over one
	// that merely starts it: with both "Anna Andersson" and "Anna Anderssen"
	// in the house, typing the first name in full means the first person.
	several := "member.several.matching"
	if exact := exactly(candidates, typed); len(exact) > 0 {
		candidates, several = exact, "member.several.named"
	}

	switch len(candidates) {
	case 1:
		return candidates[0], ""
	case 0:
		return mattermost.User{}, i18n.T(lang, "member.unknown", typed)
	default:
		// Never guess between people. Naming them turns the dead end into a
		// choice: one more letter, or a click in the list, settles it.
		return mattermost.User{}, i18n.T(lang, several, typed, describe(lang, candidates))
	}
}

// resolveLeader turns what the teams form posted into the account the list
// will be sent to.
//
// An empty field is allowed and means the team has nobody to tell yet — the
// schedule shows that as an evening with no leader rather than refusing to
// save the team. Anything else has to be a real, active person: a team leader
// who is a typo is worse than one who is missing, because the site would go on
// claiming the list had been sent.
func (s *Server) resolveLeader(ctx context.Context, lang i18n.Lang, typed string) (mattermost.User, string) {
	if strings.TrimSpace(typed) == "" {
		return mattermost.User{}, ""
	}
	return s.findMember(ctx, lang, typed)
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

// describe lists people the way the pages talk about them, so an ambiguous
// term reads as a choice rather than a rejection.
func describe(lang i18n.Lang, users []mattermost.User) string {
	const most = 5
	var names []string
	for i, u := range users {
		if i == most {
			names = append(names, i18n.T(lang, "member.andmore"))
			break
		}
		names = append(names, u.DisplayName()+" (@"+u.Username+")")
	}
	return strings.Join(names, ", ")
}

// asUsername makes a household key out of whatever was typed when there is no
// chat server to look it up in. A full name becomes the username it probably
// is — "Anna Andersson" is "anna.andersson" — because the household reads this
// back on its own page, and a key with a space in it looks like a mistake.
func asUsername(typed string) string {
	return store.Member(mattermost.Username(strings.Join(strings.Fields(typed), ".")))
}

// looksLikeUsername reports whether a value could be a Mattermost username at
// all. Names with spaces or accents never are.
func looksLikeUsername(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
