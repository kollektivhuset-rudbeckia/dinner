// Package auth implements the house's shared-password gate. There are no user
// accounts: one password lets a member in, an optional second one unlocks the
// admin view, and a member says who they are by giving their e-mail address.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
)

// Role is what a session is allowed to do.
type Role string

const (
	RoleNone   Role = ""
	RoleMember Role = "member"
	RoleAdmin  Role = "admin"
)

// Admin reports whether the role may manage teams, seasons and the schedule.
func (r Role) Admin() bool { return r == RoleAdmin }

// LoggedIn reports whether the role passed the password gate.
func (r Role) LoggedIn() bool { return r == RoleMember || r == RoleAdmin }

const (
	sessionCookie = "rb_session"
	identCookie   = "rb_ident"
)

// Guard validates passwords, issues session cookies and signs the capability
// links that let a cooking-team leader open one dinner's list from an e-mail.
type Guard struct {
	password      string
	adminPassword string
	secret        []byte
	maxAge        time.Duration
	secure        bool
}

// New builds a Guard. secure marks cookies Secure, which is right behind HTTPS.
func New(password, adminPassword string, secret []byte, maxAge time.Duration, secure bool) *Guard {
	return &Guard{password: password, adminPassword: adminPassword, secret: secret, maxAge: maxAge, secure: secure}
}

// HasAdmin reports whether an admin password was configured at all.
func (g *Guard) HasAdmin() bool { return g.adminPassword != "" }

// Check returns the role a password grants, or RoleNone.
func (g *Guard) Check(password string) Role {
	// Compare both every time so timing does not reveal which one matched.
	admin := g.adminPassword != "" && subtle.ConstantTimeCompare([]byte(password), []byte(g.adminPassword)) == 1
	member := subtle.ConstantTimeCompare([]byte(password), []byte(g.password)) == 1
	switch {
	case admin:
		return RoleAdmin
	case member:
		return RoleMember
	default:
		return RoleNone
	}
}

// Issue writes the session cookie for a role.
func (g *Guard) Issue(w http.ResponseWriter, role Role) {
	exp := time.Now().Add(g.maxAge)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    g.sign(string(role), exp),
		Path:     "/",
		Expires:  exp,
		MaxAge:   int(g.maxAge.Seconds()),
		HttpOnly: true,
		Secure:   g.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Clear removes the session cookie. The identity cookie is left alone, so
// logging back in does not mean typing your e-mail address again.
func (g *Guard) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   g.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Role reads and verifies the session cookie on a request.
func (g *Guard) Role(r *http.Request) Role {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return RoleNone
	}
	role, ok := g.verify(c.Value)
	if !ok {
		return RoleNone
	}
	switch Role(role) {
	case RoleAdmin:
		// An admin session is void if the admin password was removed.
		if g.adminPassword == "" {
			return RoleNone
		}
		return RoleAdmin
	case RoleMember:
		return RoleMember
	}
	return RoleNone
}

// Identity is who the member says they are. The e-mail address is the
// identifier that ties a registration to a household; it is never shown to
// anyone but the member themselves and the administrator.
type Identity struct {
	Name      string
	Apartment string
	Email     string
}

// Known reports whether the member has told us who they are.
func (i Identity) Known() bool { return i.Email != "" && i.Name != "" }

// Remember stores the member's details in a signed, long-lived cookie.
func (g *Guard) Remember(w http.ResponseWriter, id Identity) {
	raw := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte(id.Name)),
		base64.RawURLEncoding.EncodeToString([]byte(id.Apartment)),
		base64.RawURLEncoding.EncodeToString([]byte(id.Email)),
	}, "~")
	exp := time.Now().Add(365 * 24 * time.Hour)
	http.SetCookie(w, &http.Cookie{
		Name:     identCookie,
		Value:    g.sign(raw, exp),
		Path:     "/",
		Expires:  exp,
		MaxAge:   365 * 24 * 3600,
		HttpOnly: true,
		Secure:   g.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Forget drops the remembered identity, for a shared computer.
func (g *Guard) Forget(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     identCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   g.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Identity reads the remembered member details.
func (g *Guard) Identity(r *http.Request) Identity {
	c, err := r.Cookie(identCookie)
	if err != nil {
		return Identity{}
	}
	raw, ok := g.verify(c.Value)
	if !ok {
		return Identity{}
	}
	parts := strings.Split(raw, "~")
	if len(parts) != 3 {
		return Identity{}
	}
	dec := func(s string) string {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return ""
		}
		return string(b)
	}
	return Identity{Name: dec(parts[0]), Apartment: dec(parts[1]), Email: dec(parts[2])}
}

// Key signs a capability: a link that grants access to one thing without a
// password. It is how the mail to a cooking-team leader can point straight at
// that dinner's list.
func (g *Guard) Key(purpose, subject string, ttl time.Duration) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(purpose + "|" + subject))
	return g.sign(payload, time.Now().Add(ttl))
}

// CheckKey verifies a capability produced by Key.
func (g *Guard) CheckKey(purpose, subject, key string) bool {
	payload, ok := g.verify(key)
	if !ok {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	// Constant time is not strictly needed for a value the caller already
	// knows, but it costs nothing and keeps every comparison here uniform.
	want := purpose + "|" + subject
	return subtle.ConstantTimeCompare(raw, []byte(want)) == 1
}

func (g *Guard) sign(payload string, exp time.Time) string {
	body := payload + "." + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, g.secret)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (g *Guard) verify(value string) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", false
	}
	body := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, g.secret)
	mac.Write([]byte(body))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[2])) != 1 {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().After(time.Unix(exp, 0)) {
		return "", false
	}
	return parts[0], true
}

// Token returns a random URL-safe token, used so a guest can come back and
// change or withdraw their own registration.
func Token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is fatal for anything security related.
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// ID returns a random identifier for a registration.
func ID() string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// NormalizeEmail lower-cases and trims an address so the same household is
// always the same key, whatever they typed.
func NormalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidEmail reports whether s looks like a deliverable address.
func ValidEmail(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 254 || strings.ContainsAny(s, " \t\r\n,;") {
		return false
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s {
		return false
	}
	at := strings.LastIndexByte(s, '@')
	// A bare "user@host" is not deliverable off the machine; insist on a dot
	// in the domain so a typo does not silently swallow the confirmation.
	return at > 0 && strings.Contains(s[at+1:], ".")
}
