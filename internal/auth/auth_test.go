package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func guard() *Guard {
	secret := sha256.Sum256([]byte("test"))
	return New("husets-losenord", "admin-losenord", secret[:], 24*time.Hour, false)
}

func TestCheckGrantsTheRightRole(t *testing.T) {
	g := guard()
	tests := []struct {
		password string
		want     Role
	}{
		{"husets-losenord", RoleMember},
		{"admin-losenord", RoleAdmin},
		{"", RoleNone},
		{"husets-losenor", RoleNone},
		{"HUSETS-LOSENORD", RoleNone},
	}
	for _, tc := range tests {
		if got := g.Check(tc.password); got != tc.want {
			t.Errorf("Check(%q) = %q, want %q", tc.password, got, tc.want)
		}
	}
}

// Without an admin password the admin view must be unreachable, not open.
func TestNoAdminPasswordMeansNoAdmin(t *testing.T) {
	secret := sha256.Sum256([]byte("test"))
	g := New("hus", "", secret[:], time.Hour, false)
	if g.HasAdmin() {
		t.Error("HasAdmin should be false")
	}
	if got := g.Check(""); got != RoleNone {
		t.Errorf("an empty password must not match the empty admin password, got %q", got)
	}
}

func issue(t *testing.T, g *Guard, role Role) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	g.Issue(rec, role)
	r := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

func TestSessionRoundTrip(t *testing.T) {
	g := guard()
	if got := g.Role(issue(t, g, RoleMember)); got != RoleMember {
		t.Errorf("Role = %q, want member", got)
	}
	if got := g.Role(issue(t, g, RoleAdmin)); got != RoleAdmin {
		t.Errorf("Role = %q, want admin", got)
	}
	if got := g.Role(httptest.NewRequest("GET", "/", nil)); got != RoleNone {
		t.Errorf("no cookie: Role = %q, want none", got)
	}
}

// A cookie signed with another secret — a different house, or a rotated
// password — must not be accepted.
func TestSessionFromAnotherSecretIsRejected(t *testing.T) {
	other := sha256.Sum256([]byte("someone else"))
	stranger := New("husets-losenord", "admin-losenord", other[:], time.Hour, false)
	if got := guard().Role(issue(t, stranger, RoleAdmin)); got != RoleNone {
		t.Errorf("Role = %q, want none", got)
	}
}

func TestTamperedSessionIsRejected(t *testing.T) {
	g := guard()
	rec := httptest.NewRecorder()
	g.Issue(rec, RoleMember)
	raw := rec.Result().Cookies()[0].Value

	// Promote member to admin without re-signing.
	forged := strings.Replace(raw, "member", "admin", 1)
	for _, value := range []string{forged, "admin.9999999999.notasignature", "rubbish", "a.b"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: "rb_session", Value: value})
		if got := g.Role(r); got != RoleNone {
			t.Errorf("Role(%q) = %q, want none", value, got)
		}
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	secret := sha256.Sum256([]byte("test"))
	g := New("hus", "adm", secret[:], -time.Hour, false)
	if got := g.Role(issue(t, g, RoleMember)); got != RoleNone {
		t.Errorf("Role = %q, want none for an expired cookie", got)
	}
}

// Removing the admin password should void the admin sessions already out there.
func TestAdminSessionDiesWithTheAdminPassword(t *testing.T) {
	g := guard()
	r := issue(t, g, RoleAdmin)
	secret := sha256.Sum256([]byte("test"))
	withoutAdmin := New("husets-losenord", "", secret[:], 24*time.Hour, false)
	if got := withoutAdmin.Role(r); got != RoleNone {
		t.Errorf("Role = %q, want none", got)
	}
}

func TestIdentityRoundTrip(t *testing.T) {
	g := guard()
	id := Identity{Name: "Anna Andersson", Apartment: "1403",
		MMUsername: "anna.andersson", MMUserID: "u-anna"}
	rec := httptest.NewRecorder()
	g.Remember(rec, id)
	r := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	got := g.Identity(r)
	if got != id {
		t.Errorf("Identity = %+v, want %+v", got, id)
	}
	if !got.Known() {
		t.Error("Known should be true")
	}
	if (Identity{Name: "Anna"}).Known() {
		t.Error("a name without an account is not enough to register")
	}
	if (Identity{MMUsername: "anna.andersson"}).Known() {
		t.Error("an account with no name is not enough either")
	}
	if (Identity{}).Known() {
		t.Error("an empty identity is not known")
	}
}

// Names contain separators and non-ASCII; the encoding must survive them.
func TestIdentitySurvivesAwkwardValues(t *testing.T) {
	g := guard()
	id := Identity{Name: "Åsa ~ Öberg-Näs", Apartment: "1.2~3",
		MMUsername: "asa.oberg-nas", MMUserID: "u~1.2"}
	rec := httptest.NewRecorder()
	g.Remember(rec, id)
	r := httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	if got := g.Identity(r); got != id {
		t.Errorf("Identity = %+v, want %+v", got, id)
	}
}

// A cookie from the version that identified households by e-mail address had
// three parts where there are now four. It must be forgotten outright: reading
// the address as a username would tie the household to an account that is not
// theirs.
func TestAnIdentityFromTheEmailEraIsForgotten(t *testing.T) {
	g := guard()
	raw := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte("Anna Andersson")),
		base64.RawURLEncoding.EncodeToString([]byte("1403")),
		base64.RawURLEncoding.EncodeToString([]byte("anna@example.se")),
	}, "~")
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: identCookie, Value: g.sign(raw, time.Now().Add(time.Hour))})
	if got := g.Identity(r); got.Known() || got != (Identity{}) {
		t.Errorf("Identity = %+v, want an empty one", got)
	}
}

// The link sent to a cooking-team leader opens exactly one evening's list.
func TestCapabilityKeyIsBoundToItsSubject(t *testing.T) {
	g := guard()
	key := g.Key("lista", "2026-08-25", time.Hour)

	if !g.CheckKey("lista", "2026-08-25", key) {
		t.Error("the key should open its own dinner")
	}
	if g.CheckKey("lista", "2026-08-27", key) {
		t.Error("the key must not open another dinner")
	}
	if g.CheckKey("admin", "2026-08-25", key) {
		t.Error("the key must not work for another purpose")
	}
	if g.CheckKey("lista", "2026-08-25", "nonsense") {
		t.Error("rubbish must not be accepted")
	}
	if g.CheckKey("lista", "2026-08-25", "") {
		t.Error("an empty key must not be accepted")
	}
}

func TestCapabilityKeyExpires(t *testing.T) {
	g := guard()
	key := g.Key("lista", "2026-08-25", -time.Second)
	if g.CheckKey("lista", "2026-08-25", key) {
		t.Error("an expired key must not be accepted")
	}
}

func TestCapabilityKeyFromAnotherSecretIsRejected(t *testing.T) {
	other := sha256.Sum256([]byte("elsewhere"))
	stranger := New("hus", "adm", other[:], time.Hour, false)
	key := stranger.Key("lista", "2026-08-25", time.Hour)
	if guard().CheckKey("lista", "2026-08-25", key) {
		t.Error("a key signed elsewhere must not be accepted")
	}
}

func TestTokensAreRandomAndURLSafe(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		for _, s := range []string{Token(), ID()} {
			if seen[s] {
				t.Fatalf("repeated value %q", s)
			}
			seen[s] = true
			if strings.ContainsAny(s, "+/=&?#") {
				t.Errorf("%q is not safe in a URL", s)
			}
		}
	}
}
