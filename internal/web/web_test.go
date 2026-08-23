package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/O5ten/dinners/internal/auth"
	"github.com/O5ten/dinners/internal/config"
	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/mattermost"
	"github.com/O5ten/dinners/internal/store"
)

// The tests pretend it is Thursday 20 August 2026, in the middle of a season
// that runs Tuesdays and Thursdays. With the house's Friday-night deadline,
// the dinner on Tuesday 25 August is still open (it closes Friday 21 August)
// and the one on Thursday 20 August closed a week ago.
var (
	testNow  = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	openDay  = "2026-08-25"
	shutDay  = "2026-08-20"
	laterDay = "2026-09-01"
)

const testConfig = `
site:
  title: Rudbeckia middagar
  house_name: Kollektivhuset Rudbeckia
  timezone: Europe/Stockholm
dinner:
  weekdays: [tisdag, torsdag]
  serving_time: "18:00"
  location: stora matsalen
deadline:
  weekday: fredag
  time: "23:59"
  weeks_before: 1
`

type harness struct {
	*Server
	store *store.Store
	teams []int64
	// chat is the house's fake Mattermost, or nil when the site runs without
	// one — which is what most of these tests do, since nearly every page has
	// nothing to do with the chat server.
	chat *fakeMattermost
}

// newHarness builds a site with no chat server: lists are only logged.
func newHarness(t *testing.T) *harness { return newHarnessWithChat(t, nil) }

// newChatHarness builds a site wired to a fake Mattermost, for the parts that
// really do send something.
func newChatHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWithChat(t, newFakeMattermost(t))
}

func newHarnessWithChat(t *testing.T, chat *fakeMattermost) *harness {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(testConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	rt := config.Runtime{BaseURL: "https://mat.example.se", TrustProxy: true}
	if chat != nil {
		rt.Mattermost = config.MattermostSettings{URL: chat.URL, Token: "tok"}
	}
	secret := [32]byte{7}
	guard := auth.New("hus", "adm", secret[:], time.Hour, false)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(cfg, rt, st, guard,
		mattermost.New(rt.Mattermost.URL, rt.Mattermost.Token, log), log)
	if err != nil {
		t.Fatal(err)
	}
	srv.now = func() time.Time { return testNow }

	ctx := context.Background()
	h := &harness{Server: srv, store: st, chat: chat}
	// The two teams are led by two of the people in the fake directory, so a
	// list that goes out has somebody real to go to.
	for i, team := range []struct{ name, leader, username string }{
		{"Lag 1", "Mikael Östberg", "mikael.ostberg"},
		{"Lag 2", "Cecilia Dahl", "cecilia.dahl"},
	} {
		id, err := st.SaveTeam(ctx, store.Team{
			Name: team.name, LeaderName: team.leader, LeaderUsername: team.username,
			Position: i, Active: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		h.teams = append(h.teams, id)
	}
	if _, err := st.SaveSeason(ctx, store.Season{
		Name: "Test", Start: "2026-08-01", End: "2026-12-31",
		Weekdays: []time.Weekday{time.Tuesday, time.Thursday},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings(ctx, store.Settings{
		DeadlineWeekday: time.Friday, DeadlineMinutes: 23*60 + 59,
		DeadlineWeeksBefore: 1, GuestOpen: true,
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

// client keeps cookies between requests, the way a browser does.
type client struct {
	t       *testing.T
	handler http.Handler
	cookies map[string]string
}

func (h *harness) client(t *testing.T) *client {
	return &client{t: t, handler: h.Handler(), cookies: map[string]string{}}
}

func (c *client) do(method, target string, form url.Values) *httptest.ResponseRecorder {
	c.t.Helper()
	var req *http.Request
	if form == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for name, value := range c.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 {
			delete(c.cookies, ck.Name)
			continue
		}
		c.cookies[ck.Name] = ck.Value
	}
	return rec
}

func (c *client) get(target string) *httptest.ResponseRecorder { return c.do("GET", target, nil) }
func (c *client) post(target string, form url.Values) *httptest.ResponseRecorder {
	return c.do("POST", target, form)
}

func (c *client) login(password string) {
	c.t.Helper()
	rec := c.post("/login", url.Values{"password": {password}})
	if rec.Code != http.StatusSeeOther {
		c.t.Fatalf("login: %d", rec.Code)
	}
}

// identify says who this browser is: a Mattermost username, and the name the
// cooking team reads on the list.
func (c *client) identify(name, username string) {
	c.t.Helper()
	rec := c.post("/jagar", url.Values{"name": {name}, "member": {username}, "next": {"/"}})
	if rec.Code != http.StatusSeeOther {
		c.t.Fatalf("identify: %d — %s", rec.Code, rec.Body.String())
	}
}

func (c *client) member(name, username string) {
	c.t.Helper()
	c.login("hus")
	c.identify(name, username)
}

// party is the shared part of every registration form: how many people, what
// they eat, and anything the cooking team should know.
func party(adults, children int, diet store.Diet, note string) url.Values {
	return url.Values{
		"adults": {itoa(adults)}, "children": {itoa(children)},
		"diet": {string(diet)}, "note": {note},
	}
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Replace(" "+string(rune('0'+n)), " ", "", 1))
}

// ---------------------------------------------------------------- the gate --

func TestEverythingIsBehindThePassword(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	for _, path := range []string{"/", "/mina", "/middag/" + openDay, "/admin"} {
		rec := c.get(path)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("GET %s = %d, want a redirect to the login page", path, rec.Code)
		}
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
			t.Errorf("GET %s redirected to %q", path, loc)
		}
	}
}

func TestWrongPasswordIsRefusedAndThrottled(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	rec := c.post("/login", url.Values{"password": {"fel"}})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", rec.Code)
	}
	if len(c.cookies) != 0 {
		t.Errorf("a failed login must not set a cookie: %v", c.cookies)
	}
}

// Nothing can be registered before we know whose registration it is.
func TestAMemberMustSayWhoTheyAre(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.login("hus")

	rec := c.get("/")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/jagar") {
		t.Fatalf("expected a redirect to /jagar, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// Saying nothing is refused rather than quietly stored.
	rec = c.post("/jagar", url.Values{"name": {"Anna"}, "member": {""}, "next": {"/"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("no account = %d, want 422", rec.Code)
	}
	// A pasted mention with capitals is the same household as the username.
	c.identify("Anna Andersson", "@Anna.Andersson")
	if rec := c.get("/"); rec.Code != http.StatusOK {
		t.Errorf("GET / after identifying = %d", rec.Code)
	}
}

func TestMemberCannotReachTheAdminView(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna.andersson")
	if rec := c.get("/admin"); rec.Code != http.StatusForbidden {
		t.Errorf("member at /admin = %d, want 403", rec.Code)
	}
	admin := h.client(t)
	admin.login("adm")
	admin.identify("Chef", "cecilia.dahl")
	if rec := admin.get("/admin"); rec.Code != http.StatusOK {
		t.Errorf("admin at /admin = %d", rec.Code)
	}
}

// ------------------------------------------------------------- registering --

func TestRegisterForOneDinner(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna Andersson", "anna.andersson")

	rec := c.post("/middag/"+openDay, party(2, 1, store.DietVegetarian, "glutenfritt för ett barn"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("register = %d — %s", rec.Code, rec.Body.String())
	}

	got, err := h.store.MemberRegistration(context.Background(), openDay, "anna.andersson")
	if err != nil {
		t.Fatalf("stored registration: %v", err)
	}
	if got.Adults != 2 || got.Children != 1 || got.Diet != store.DietVegetarian {
		t.Errorf("stored %+v", got)
	}
	if got.Name != "Anna Andersson" {
		t.Errorf("Name = %q, want the identity's name", got.Name)
	}

	body := c.get("/middag/" + openDay).Body.String()
	if !strings.Contains(body, "Ni är anmälda") {
		t.Error("the page should say the household is signed up")
	}
}

func TestRegistrationIsRefusedAfterTheDeadline(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna.andersson")

	rec := c.post("/middag/"+shutDay, party(2, 0, store.DietOmnivore, ""))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("late registration = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "stängde") {
		t.Error("the page should explain that registration has closed")
	}
	if _, err := h.store.MemberRegistration(context.Background(), shutDay, "anna.andersson"); err == nil {
		t.Error("nothing should have been stored")
	}
}

func TestImpossibleRegistrationsAreRefused(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna.andersson")

	tests := []struct {
		name string
		form url.Values
	}{
		{"negative", url.Values{"adults": {"-3"}, "children": {"0"}, "diet": {"allatare"}}},
		{"absurdly many", url.Values{"adults": {"400"}, "children": {"0"}, "diet": {"allatare"}}},
		// Refused rather than quietly served the unrestricted meal, which is
		// the one silent mistake here that matters.
		{"a diet nobody offers", url.Values{"adults": {"2"}, "diet": {"makrobiotisk"}}},
		{"no diet at all", url.Values{"adults": {"2"}}},
		{"a note longer than the field allows", url.Values{
			"adults": {"1"}, "diet": {"allatare"},
			"note": {strings.Repeat("ä", 301)},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := c.post("/middag/"+openDay, tc.form)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("= %d, want 422", rec.Code)
			}
			if _, err := h.store.MemberRegistration(context.Background(), openDay, "anna.andersson"); err == nil {
				t.Error("nothing should have been stored")
			}
		})
	}
}

// The standing registration is the replacement for the permanent-registration
// sheet: set it once and you are counted every week.
func TestStandingRegistrationCountsYouInAutomatically(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna Andersson", "anna.andersson")

	rec := c.post("/stadigvarande", withWeekday(party(2, 2, store.DietOmnivore, "nötallergi"), time.Tuesday))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save standing = %d — %s", rec.Code, rec.Body.String())
	}

	// Nothing was written for the individual evening...
	if _, err := h.store.MemberRegistration(context.Background(), openDay, "anna.andersson"); err == nil {
		t.Error("a standing registration should not write a row per dinner")
	}
	// ...but the household is on the list all the same.
	body := c.get("/middag/" + openDay).Body.String()
	if !strings.Contains(body, "Ni är anmälda") {
		t.Error("the standing registration should show up on the evening")
	}
	list := c.get("/middag/" + openDay + "/lista").Body.String()
	if !strings.Contains(list, "Anna Andersson") {
		t.Error("the household should be on the cooking team's list")
	}
	if !strings.Contains(list, "nötallergi") {
		t.Error("the standing note should reach the list")
	}
}

func TestSkippingOneEveningBeatsTheStandingRegistration(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna.andersson")
	c.post("/stadigvarande", withWeekday(party(2, 0, store.DietOmnivore, ""), time.Tuesday))

	rec := c.post("/middag/"+openDay, url.Values{"action": {"decline"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("decline = %d", rec.Code)
	}

	list := c.get("/middag/" + openDay + "/lista").Body.String()
	if !strings.Contains(list, "Ingen har anmält sig") {
		t.Error("the only household said no, so nobody should be attending")
	}
	if !strings.Contains(list, "Har tackat nej") {
		t.Error("the cooking team should see that the answer was given")
	}
	// The next Tuesday is untouched by one evening's answer.
	later := c.get("/middag/" + laterDay + "/lista").Body.String()
	if !strings.Contains(later, "Anna") {
		t.Error("the standing registration should still hold for other evenings")
	}
}

func TestRemovingTheStandingRegistration(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna.andersson")
	c.post("/stadigvarande", withWeekday(party(2, 0, store.DietOmnivore, ""), time.Tuesday))

	if list, _ := h.store.StandingByMember(context.Background(), "anna.andersson"); len(list) != 1 {
		t.Fatal("standing registration was not saved")
	}
	rec := c.post("/stadigvarande", withWeekday(url.Values{"action": {"clear"}}, time.Tuesday))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clear = %d", rec.Code)
	}
	if list, _ := h.store.StandingByMember(context.Background(), "anna.andersson"); len(list) != 0 {
		t.Errorf("standing registration should be gone, got %+v", list)
	}
	if strings.Contains(c.get("/middag/"+openDay+"/lista").Body.String(), "Anna") {
		t.Error("the household should no longer be counted")
	}
}

// dinner finds one evening in the generated schedule.
func (h *harness) dinner(t *testing.T, key string) dinner.Dinner {
	t.Helper()
	world, err := h.world(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d, ok := world.Schedule.Find(key)
	if !ok {
		t.Fatalf("no dinner on %s", key)
	}
	return d
}

func withWeekday(v url.Values, wd time.Weekday) url.Values {
	v.Set("weekday", itoa(int(wd)))
	return v
}

// One household's address is nobody else's business.
func TestOtherMembersNeverSeeYourAccount(t *testing.T) {
	h := newHarness(t)
	anna := h.client(t)
	anna.member("Anna Andersson", "anna.andersson")
	anna.post("/middag/"+openDay, party(2, 0, store.DietOmnivore, ""))

	bo := h.client(t)
	bo.member("Bo Bengtsson", "bo.bengtsson")
	for _, path := range []string{"/", "/middag/" + openDay, "/middag/" + openDay + "/lista", "/mina"} {
		if body := bo.get(path).Body.String(); strings.Contains(body, "anna.andersson") {
			t.Errorf("%s leaks Anna's username", path)
		}
	}
	// Her own page still shows it to her.
	if !strings.Contains(anna.get("/mina").Body.String(), "anna.andersson") {
		t.Error("a member should see their own username")
	}
}

// ------------------------------------------------------------- the matlist --

func TestListNeedsAPasswordOrTheSignedKey(t *testing.T) {
	h := newHarness(t)
	path := "/middag/" + openDay + "/lista"

	stranger := h.client(t)
	if rec := stranger.get(path); rec.Code != http.StatusSeeOther {
		t.Errorf("stranger = %d, want a redirect to the login page", rec.Code)
	}

	// The signed link from the cooking team's mail opens this one evening.
	key := h.guard.Key(listKeyPurpose, openDay, time.Hour)
	rec := stranger.get(path + "?nyckel=" + url.QueryEscape(key))
	if rec.Code != http.StatusOK {
		t.Errorf("with the mailed key = %d, want 200", rec.Code)
	}
	// And nothing else.
	rec = stranger.get("/middag/" + laterDay + "/lista?nyckel=" + url.QueryEscape(key))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("the key opened another evening: %d", rec.Code)
	}
	rec = stranger.get(path + "?nyckel=rubbish")
	if rec.Code != http.StatusSeeOther {
		t.Errorf("a forged key was accepted: %d", rec.Code)
	}
	// A capability link is not a way into the rest of the house.
	if rec := stranger.get("/"); rec.Code != http.StatusSeeOther {
		t.Errorf("the key should not unlock the site: %d", rec.Code)
	}
}

func TestListShowsTheAggregatesTheCookingTeamNeeds(t *testing.T) {
	h := newHarness(t)
	anna := h.client(t)
	anna.member("Anna", "anna.andersson")
	anna.post("/middag/"+openDay, party(2, 2, store.DietVegetarian, "glutenfritt"))
	bo := h.client(t)
	bo.member("Bo", "bo.bengtsson")
	bo.post("/middag/"+openDay, party(1, 0, store.DietVegan, ""))

	body := anna.get("/middag/" + openDay + "/lista").Body.String()
	for _, want := range []string{"Matlista", "glutenfritt", "Anna", "Bo"} {
		if !strings.Contains(body, want) {
			t.Errorf("the list is missing %q", want)
		}
	}
	// Five people across two households.
	if !strings.Contains(body, "5 personer") {
		t.Error("the total is missing from the list")
	}
}

// --------------------------------------------------------------- the guest --

func TestGuestRegistersWithoutAPassword(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)

	if rec := c.get("/gast"); rec.Code != http.StatusOK {
		t.Fatalf("the guest page needs no password, got %d", rec.Code)
	}
	form := party(2, 0, store.DietVegan, "vegan")
	form.Set("date", openDay)
	form.Set("name", "Kalle Svensson")
	form.Set("host", "Anna Andersson")
	rec := c.post("/gast", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("guest registration = %d — %s", rec.Code, rec.Body.String())
	}
	link := rec.Header().Get("Location")
	if !strings.HasPrefix(link, "/gast/") {
		t.Fatalf("expected a link back to the guest's own registration, got %q", link)
	}

	regs, _ := h.store.Registrations(context.Background(), openDay)
	if len(regs) != 1 || regs[0].Kind != store.KindGuest || regs[0].Name != "Kalle Svensson" {
		t.Fatalf("stored %+v", regs)
	}

	// The guest can come back and change their mind.
	if body := c.get(link).Body.String(); !strings.Contains(body, "Kalle Svensson") {
		t.Error("the guest should see their own registration")
	}
	if rec := c.post(strings.Split(link, "?")[0], url.Values{"action": {"delete"}}); rec.Code != http.StatusOK {
		t.Fatalf("withdraw = %d", rec.Code)
	}
	regs, _ = h.store.Registrations(context.Background(), openDay)
	if len(regs) != 0 {
		t.Errorf("the registration should be gone, got %+v", regs)
	}
}

func TestGuestCannotRegisterForAClosedDinner(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	form := party(1, 0, store.DietOmnivore, "")
	form.Set("date", shutDay)
	form.Set("name", "Kalle")
	if rec := c.post("/gast", form); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("= %d, want 422", rec.Code)
	}
}

func TestGuestPageCanBeTurnedOff(t *testing.T) {
	h := newHarness(t)
	if err := h.store.SaveSettings(context.Background(), store.Settings{
		DeadlineWeekday: time.Friday, DeadlineMinutes: 23*60 + 59,
		DeadlineWeeksBefore: 1, GuestOpen: false,
	}); err != nil {
		t.Fatal(err)
	}
	c := h.client(t)
	if rec := c.get("/gast"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /gast = %d, want 404 when guests are turned off", rec.Code)
	}
	form := party(1, 0, store.DietOmnivore, "")
	form.Set("date", openDay)
	form.Set("name", "Kalle")
	if rec := c.post("/gast", form); rec.Code != http.StatusNotFound {
		t.Errorf("POST /gast = %d, want 404", rec.Code)
	}
}

// A guest is nobody's business but the cooking team's, and the guest form must
// not become a way to read the house's list.
func TestGuestPageShowsNobodyElse(t *testing.T) {
	h := newHarness(t)
	anna := h.client(t)
	anna.member("Zäta Zettergren", "zeta.zettergren")
	anna.post("/middag/"+openDay, party(2, 0, store.DietOmnivore, "kikärtsallergi"))

	body := h.client(t).get("/gast").Body.String()
	for _, secret := range []string{"Zäta Zettergren", "zeta.zettergren", "kikärtsallergi"} {
		if strings.Contains(body, secret) {
			t.Errorf("the public guest page leaks %q", secret)
		}
	}
}

// ---------------------------------------------------------------- the admin --

func TestAdminManagesTeamsSeasonsAndBreaks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")

	// A new team.
	rec := c.post("/admin/lag", url.Values{
		"name": {"Lag 3"}, "leader_username": {"cecilia.dahl"},
		"position": {"2"}, "active": {"1"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save team = %d — %s", rec.Code, rec.Body.String())
	}
	teams, _ := h.store.Teams(ctx)
	if len(teams) != 3 {
		t.Fatalf("got %d teams, want 3", len(teams))
	}

	// A team is saved with the leader's Mattermost username, since that is the
	// only way the site can reach anybody. This site has no chat server to ask
	// what she is called, so it invents nothing and leaves the name empty.
	if teams[2].LeaderUsername != "cecilia.dahl" || teams[2].LeaderName != "" {
		t.Errorf("Lag 3 was saved as %+v", teams[2])
	}

	// A break.
	rec = c.post("/admin/uppehall", url.Values{
		"name": {"Höstlov"}, "start": {"2026-08-24"}, "end": {"2026-08-30"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save break = %d — %s", rec.Code, rec.Body.String())
	}
	// The evening inside it is gone.
	if rec := c.get("/middag/" + openDay); rec.Code != http.StatusNotFound {
		t.Errorf("a dinner inside a break = %d, want 404", rec.Code)
	}

	breaks, _ := h.store.Breaks(ctx)
	if len(breaks) != 1 {
		t.Fatalf("got %d breaks", len(breaks))
	}
	if rec := c.post("/admin/uppehall", url.Values{
		"id": {itoa(int(breaks[0].ID))}, "action": {"delete"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete break = %d", rec.Code)
	}
	if rec := c.get("/middag/" + openDay); rec.Code != http.StatusOK {
		t.Errorf("removing the break should bring the dinner back, got %d", rec.Code)
	}
}

func TestAdminCancelsAnEveningAndSwapsATeam(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")

	rec := c.post("/admin/schema", url.Values{
		"date": {openDay}, "cancelled": {"1"}, "note": {"Festkommittén har lokalen"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("cancel = %d", rec.Code)
	}
	body := c.get("/middag/" + openDay).Body.String()
	if !strings.Contains(body, "inställd") && !strings.Contains(body, "Inställd") {
		t.Error("the evening should say it is cancelled")
	}

	// Nobody can register for a cancelled evening.
	member := h.client(t)
	member.member("Anna", "anna.andersson")
	if rec := member.post("/middag/"+openDay, party(2, 0, store.DietOmnivore, "")); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("registering for a cancelled dinner = %d, want 422", rec.Code)
	}

	// Hand the next evening to the other team.
	rec = c.post("/admin/schema", url.Values{
		"date": {laterDay}, "team_id": {itoa(int(h.teams[1]))},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("swap = %d", rec.Code)
	}
	world, err := h.world(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d, _ := world.Schedule.Find(laterDay)
	if d.Team == nil || d.Team.Name != "Lag 2" || !d.Assigned {
		t.Errorf("swapped evening = %+v", d.Team)
	}
}

func TestAdminSettingsMoveTheDeadline(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")

	rec := c.post("/admin/installningar", url.Values{
		"deadline_weekday":      {itoa(int(time.Wednesday))},
		"deadline_time":         {"10:00"},
		"deadline_weeks_before": {"0"},
		"guest_open":            {"1"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save settings = %d — %s", rec.Code, rec.Body.String())
	}
	world, _ := h.world(context.Background())
	d, _ := world.Schedule.Find(openDay)
	// Tuesday 25 August, deadline Wednesday of its own week: 26 August 10:00 —
	// which is after the dinner, but that is the administrator's business.
	if got := d.Closes.Format("2006-01-02 15:04"); got != "2026-08-26 10:00" {
		t.Errorf("deadline = %s", got)
	}

	if rec := c.post("/admin/installningar", url.Values{
		"deadline_weekday": {"5"}, "deadline_time": {"tjugo i fem"}, "deadline_weeks_before": {"1"},
	}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a bad time = %d, want 422", rec.Code)
	}
}

func TestAdminCSVExport(t *testing.T) {
	h := newHarness(t)
	anna := h.client(t)
	anna.member("Anna Andersson", "anna.andersson")
	anna.post("/middag/"+openDay, party(2, 1, store.DietVegetarian, "glutenfritt"))

	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")
	rec := c.get("/admin/export.csv?fran=2026-08-01&till=2026-12-31")
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"datum,veckodag", "Anna Andersson", "glutenfritt", openDay} {
		if !strings.Contains(body, want) {
			t.Errorf("the export is missing %q", want)
		}
	}
}

// ------------------------------------------------------- the notification --

// At the deadline the cooking team's leader is told what to cook — once.
func TestTheListIsSentOnceWhenRegistrationCloses(t *testing.T) {
	h := newChatHarness(t)
	ctx := context.Background()

	// Before the deadline, nothing goes out for the open evening.
	h.runNotifications(ctx)
	if done, _ := h.store.Notified(ctx, openDay, notifyKind); done {
		t.Error("the open dinner should not have been announced yet")
	}
	// The evening whose deadline has passed has been.
	done, err := h.store.Notified(ctx, shutDay, notifyKind)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("the closed dinner should have been sent")
	}
	sent, _ := h.store.SentNotifications(ctx, "2026-08-01", "2026-08-31")
	log, ok := sent[shutDay]
	if !ok {
		t.Fatalf("nothing was recorded for %s", shutDay)
	}
	// The log records the account it went to, which is what the admin view
	// shows and what a resend repeats.
	leader := h.dinner(t, shutDay).Team.LeaderUsername
	dm := h.chat.waitForDMTo(t, leader, 1)
	if log.Recipient != dm.Username {
		t.Errorf("the log says %q but the message went to %q", log.Recipient, dm.Username)
	}

	// Running again must not send a second time.
	h.runNotifications(ctx)
	if got := h.chat.messagesTo(leader); len(got) != 1 {
		t.Errorf("a second run sent %d messages, want the one", len(got))
	}
}

// The message carries the numbers the team shops by, and a link for
// everything else. Names and allergies stay on the list, where they are always
// current and belong to the households who wrote them.
func TestTheMessageCarriesTheTotalsAndALinkToTheList(t *testing.T) {
	h := newChatHarness(t)
	ctx := context.Background()

	anna := h.client(t)
	anna.member("Anna Andersson", "anna.andersson")
	anna.post("/middag/"+openDay, party(2, 1, store.DietVegetarian, "glutenfritt"))
	bo := h.client(t)
	bo.member("Bo Bengtsson", "bo.bengtsson")
	bo.post("/middag/"+openDay, party(1, 0, store.DietVegan, ""))

	d := h.dinner(t, openDay)
	if err := h.sendList(ctx, d); err != nil {
		t.Fatalf("sendList: %v", err)
	}
	// Whichever team the rotation landed on, it is that team's leader who is
	// told, and they are greeted by name. The households have been confirmed
	// separately, so the list is found by who it went to.
	dm := h.chat.waitForDMTo(t, d.Team.LeaderUsername, 1)
	for _, want := range []string{
		"Hej " + firstName(d.Team.LeaderName) + "!",
		"tisdag 25 augusti",
		"| **Hushåll** | 2 |",
		"| **Vuxna** | 3 |",
		"| **Barn** | 1 |",
		"| **Portioner** | 4 |",
		"| **Vegetarian** | 3 |",
		"| **Vegan** | 1 |",
		// Every pot is listed, even the ones nobody needs this week.
		"| **Allätare** | 0 |",
		"| **Allergier** | 1 |",
		"[Öppna matlistan](https://mat.example.se/middag/" + openDay + "/lista?nyckel=",
		"Maten serveras 18:00 i stora matsalen.",
	} {
		if !strings.Contains(dm.Message, want) {
			t.Errorf("the message is missing %q:\n%s", want, dm.Message)
		}
	}
	// And the link in it opens that one evening's list without a login.
	link := dm.Message[strings.Index(dm.Message, "https://mat.example.se"):]
	link = link[:strings.IndexByte(link, ')')]

	// What is not in the message matters as much: the households and what they
	// wrote. The signed key is opaque base64 and may spell anything at all, so
	// it is taken out before reading the words.
	words := strings.Replace(dm.Message, link, "", 1)
	for _, unwanted := range []string{"Anna", "Bo", "glutenfritt"} {
		if strings.Contains(words, unwanted) {
			t.Errorf("the message gives away %q:\n%s", unwanted, dm.Message)
		}
	}

	rec := h.client(t).get(strings.TrimPrefix(link, "https://mat.example.se"))
	if rec.Code != http.StatusOK {
		t.Fatalf("following the link from the message = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "glutenfritt") {
		t.Error("the list itself should carry the allergies")
	}
}

// An evening nobody has registered for still gets a message: the team needs to
// know that too, and a silence is indistinguishable from a broken site.
func TestAnEmptyEveningIsStillAnnounced(t *testing.T) {
	h := newChatHarness(t)
	d := h.dinner(t, shutDay)
	if err := h.sendList(context.Background(), d); err != nil {
		t.Fatalf("sendList: %v", err)
	}
	dm := h.chat.waitForDMTo(t, d.Team.LeaderUsername, 1)
	if !strings.Contains(dm.Message, "Ingen har anmält sig") {
		t.Errorf("the message should say that nobody is coming:\n%s", dm.Message)
	}
	if strings.Contains(dm.Message, "| **Portioner** |") {
		t.Errorf("there are no totals to give:\n%s", dm.Message)
	}
}

func TestCancelledEveningsAreNotAnnounced(t *testing.T) {
	h := newChatHarness(t)
	ctx := context.Background()
	if err := h.store.SaveOverride(ctx, store.Override{
		Date: shutDay, Cancelled: true, UpdatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	h.runNotifications(ctx)
	if done, _ := h.store.Notified(ctx, shutDay, notifyKind); done {
		t.Error("a cancelled dinner should not be announced")
	}
	if got := h.chat.messages(); len(got) != 0 {
		t.Errorf("a cancelled dinner sent %d messages", len(got))
	}
}

// A team with no leader username has nobody to tell. That is recorded as such,
// so the notifier stops trying and the schedule shows it as something to fix.
func TestAnEveningWithoutALeaderIsRecordedRatherThanRetried(t *testing.T) {
	h := newChatHarness(t)
	ctx := context.Background()
	teams, _ := h.store.Teams(ctx)
	for _, team := range teams {
		team.LeaderUsername = ""
		if _, err := h.store.SaveTeam(ctx, team); err != nil {
			t.Fatal(err)
		}
	}

	h.runNotifications(ctx)
	if got := h.chat.messages(); len(got) != 0 {
		t.Errorf("%d messages went out with no leader to send them to", len(got))
	}
	sent, _ := h.store.SentNotifications(ctx, "2026-08-01", "2026-08-31")
	if log, ok := sent[shutDay]; !ok || log.Recipient != noTeam {
		t.Errorf("the log should mark the evening as having nobody to tell, got %+v", log)
	}
}

// A leader who lost the message, or a list worth repeating after a late
// change, is what the schedule's "send again" is for.
func TestTheAdministratorCanSendTheListAgain(t *testing.T) {
	h := newChatHarness(t)
	ctx := context.Background()
	leader := h.dinner(t, shutDay).Team.LeaderUsername
	h.runNotifications(ctx)
	h.chat.waitForDMTo(t, leader, 1)

	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")
	rec := c.post("/admin/skicka", url.Values{"date": {shutDay}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("send again = %d — %s", rec.Code, rec.Body.String())
	}
	if again := h.chat.waitForDMTo(t, leader, 2); again.Username != leader {
		t.Errorf("the second message went to %q, want %q", again.Username, leader)
	}
	// The log still says when the leader last heard from us.
	sent, _ := h.store.SentNotifications(ctx, "2026-08-01", "2026-08-31")
	if log, ok := sent[shutDay]; !ok || log.Recipient != leader {
		t.Errorf("the log after resending = %+v", log)
	}
}

// Without a chat server nothing can be delivered. The site has to keep working
// and say so, rather than claiming the team was told.
func TestWithoutMattermostTheListIsOnlyLogged(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.runNotifications(ctx)
	if done, _ := h.store.Notified(ctx, shutDay, notifyKind); !done {
		t.Error("the notification should still be recorded so it is not retried forever")
	}
	body := h.client(t).get("/login").Body.String()
	if !strings.Contains(body, "Utan utskick") {
		t.Error("every page should say that nothing is being sent")
	}
}

// -------------------------------------------------------- naming a leader --

func TestNamingACookingTeamLeader(t *testing.T) {
	for _, tc := range []struct {
		name, typed, want string
		status            int
	}{
		{"a username", "cecilia.dahl", "cecilia.dahl", http.StatusSeeOther},
		{"a pasted mention", "@cecilia.dahl", "cecilia.dahl", http.StatusSeeOther},
		{"a full name", "Cecilia Dahl", "cecilia.dahl", http.StatusSeeOther},
		{"nobody at all", "", "", http.StatusSeeOther},
		{"a stranger", "hittepa.person", "", http.StatusUnprocessableEntity},
		// Two people are called Anna Andersson, and picking one of them is not
		// ours to do.
		{"two people at once", "Anna Andersson", "", http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newChatHarness(t)
			c := h.client(t)
			c.login("adm")
			c.identify("Chef", "cecilia.dahl")

			rec := c.post("/admin/lag", url.Values{
				"name": {"Lag 3"}, "leader_username": {tc.typed}, "active": {"1"},
			})
			if rec.Code != tc.status {
				t.Fatalf("save = %d, want %d — %s", rec.Code, tc.status, rec.Body.String())
			}
			teams, _ := h.store.Teams(context.Background())
			if tc.status != http.StatusSeeOther {
				if len(teams) != 2 {
					t.Fatalf("the team was saved anyway: %+v", teams)
				}
				return
			}
			if len(teams) != 3 {
				t.Fatalf("got %d teams", len(teams))
			}
			if teams[2].LeaderUsername != tc.want {
				t.Errorf("leader = %q, want %q", teams[2].LeaderUsername, tc.want)
			}
		})
	}
}

// Nobody types a leader's name: it is spelled the way its owner spells it on
// their account, so the schedule and the list read the way the house does. A
// name posted by an older form is ignored rather than kept alongside it.
func TestALeadersNameComesFromTheirAccount(t *testing.T) {
	h := newChatHarness(t)
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")
	c.post("/admin/lag", url.Values{
		"name": {"Lag 3"}, "leader_username": {"mikael.ostberg"}, "active": {"1"},
		"leader_name": {"Mickey"},
	})
	teams, _ := h.store.Teams(context.Background())
	if len(teams) != 3 || teams[2].LeaderName != "Mikael Östberg" {
		t.Errorf("teams = %+v", teams)
	}

	// Taking the username away takes the name with it: there is nobody to name.
	c.post("/admin/lag", url.Values{
		"id": {itoa(int(teams[2].ID))}, "name": {"Lag 3"},
		"leader_username": {""}, "active": {"1"},
	})
	teams, _ = h.store.Teams(context.Background())
	if teams[2].LeaderName != "" || teams[2].LeaderUsername != "" {
		t.Errorf("the team kept a leader it no longer has: %+v", teams[2])
	}
}

// Remembering usernames is not something anybody should have to do, so the
// pickers offer the house — once, from a cache, and only to somebody who is
// already inside the house password.
func TestThePickersOfferTheHouse(t *testing.T) {
	h := newChatHarness(t)
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")

	rec := c.get("/medlemmar")
	if rec.Code != http.StatusOK {
		t.Fatalf("member list = %d", rec.Code)
	}
	var got memberList
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — %s", err, rec.Body.String())
	}
	if len(got.Users) != 5 || got.AskServer || got.Unreachable {
		t.Fatalf("offered %+v", got)
	}
	// Sorted by the name the reader reads, and nobody who has left.
	if got.Users[0].Name != "Anna Andersson" || got.Users[len(got.Users)-1].Name != "Mikael Östberg" {
		t.Errorf("the list is not in name order: %+v", got.Users)
	}
	for _, u := range got.Users {
		if u.Username == "gammal.granne" {
			t.Error("somebody who has moved out is still offered")
		}
	}

	// Asking again reuses the listing rather than paging through the whole
	// server on every view.
	c.get("/medlemmar")
	if n := h.chat.requests("users"); n != 1 {
		t.Errorf("the directory was fetched %d times, want once", n)
	}

	// A household needs it too — that is how it says who it is — so the gate is
	// the house password rather than the admin one.
	member := h.client(t)
	member.member("Anna", "anna.andersson")
	if rec := member.get("/medlemmar"); rec.Code != http.StatusOK {
		t.Errorf("a member reading the house directory = %d, want 200", rec.Code)
	}

	// Nobody outside the house sees who lives here.
	if rec := h.client(t).get("/medlemmar"); rec.Code != http.StatusSeeOther {
		t.Errorf("a stranger reading the house directory = %d, want a redirect to the login", rec.Code)
	}

	// A name can also be searched for on the server, which is the fallback for
	// a directory too large to hold in the browser.
	rec = c.get("/medlemmar?q=" + url.QueryEscape("Östberg"))
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d", rec.Code)
	}
	var hits memberList
	if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits.Users) != 1 || hits.Users[0].Username != "mikael.ostberg" {
		t.Errorf("searching found %+v", hits.Users)
	}
}

// --------------------------------------------------------------- odds & ends --

func TestUnknownDinnerIsANotFound(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna.andersson")
	// A Wednesday, which the season does not cook on.
	if rec := c.get("/middag/2026-08-26"); rec.Code != http.StatusNotFound {
		t.Errorf("= %d, want 404", rec.Code)
	}
	if rec := c.get("/middag/inte-ett-datum"); rec.Code != http.StatusNotFound {
		t.Errorf("= %d, want 404", rec.Code)
	}
}

func TestHealthAndStatic(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	if rec := c.get("/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d", rec.Code)
	}
	if rec := c.get("/static/app.css"); rec.Code != http.StatusOK {
		t.Errorf("/static/app.css = %d", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	rec := h.client(t).get("/login")
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
}

// A redirect target that came out of a form is not to be trusted.
func TestLoginOnlyRedirectsWithinTheSite(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "/"},
		{"/mina", "/mina"},
		{"//evil.example.com", "/"},
		{"https://evil.example.com", "/"},
		{"javascript:alert(1)", "/"},
	} {
		if got := safeNext(tc.in); got != tc.want {
			t.Errorf("safeNext(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Two seasons covering the same day would disagree about who cooks it.
func TestAdminRefusesOverlappingSeasons(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")

	// The harness already has a season covering all of 2026.
	rec := c.post("/admin/sasong", url.Values{
		"name": {"Krockar"}, "start": {"2026-11-01"}, "end": {"2027-02-01"},
		"weekdays": {"2", "4"},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("overlapping season = %d, want 409 — %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "överlappar") {
		t.Error("the message should say the seasons overlap")
	}
	if all, _ := h.store.Seasons(ctx); len(all) != 1 {
		t.Errorf("nothing should have been stored, got %d seasons", len(all))
	}

	// A season starting the day after the last one ends is fine.
	rec = c.post("/admin/sasong", url.Values{
		"name": {"Våren 2027"}, "start": {"2027-01-01"}, "end": {"2027-05-31"},
		"weekdays": {"2", "4"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("adjacent season = %d — %s", rec.Code, rec.Body.String())
	}
	if all, _ := h.store.Seasons(ctx); len(all) != 2 {
		t.Errorf("got %d seasons, want 2", len(all))
	}

	// Editing a season must not find that it overlaps itself.
	all, _ := h.store.Seasons(ctx)
	rec = c.post("/admin/sasong", url.Values{
		"id": {itoa(int(all[0].ID))}, "name": {"Omdöpt"},
		"start": {all[0].Start}, "end": {all[0].End}, "weekdays": {"2", "4"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Errorf("editing a season in place = %d — %s", rec.Code, rec.Body.String())
	}
}

// The guest page is open to the internet, so the administrator needs a way to
// remove something that should not be on the list.
func TestAdminRemovesARegistrationFromTheList(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	guest := h.client(t)
	form := party(9, 0, store.DietOmnivore, "")
	form.Set("date", openDay)
	form.Set("name", "Skräppost Skräppostsson")
	if rec := guest.post("/gast", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("guest registration = %d", rec.Code)
	}
	regs, _ := h.store.Registrations(ctx, openDay)
	if len(regs) != 1 {
		t.Fatalf("got %d registrations", len(regs))
	}

	// A member cannot remove someone else's registration.
	member := h.client(t)
	member.member("Anna", "anna.andersson")
	if rec := member.post("/admin/anmalan", url.Values{"id": {regs[0].ID}}); rec.Code != http.StatusForbidden {
		t.Errorf("member deleting = %d, want 403", rec.Code)
	}
	if left, _ := h.store.Registrations(ctx, openDay); len(left) != 1 {
		t.Fatal("the registration should still be there")
	}

	admin := h.client(t)
	admin.login("adm")
	admin.identify("Chef", "cecilia.dahl")
	// The control is offered on the list.
	if body := admin.get("/middag/" + openDay + "/lista").Body.String(); !strings.Contains(body, "/admin/anmalan") {
		t.Error("the admin should be offered a way to remove a registration")
	}
	if rec := admin.post("/admin/anmalan", url.Values{"id": {regs[0].ID}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("admin deleting = %d", rec.Code)
	}
	if left, _ := h.store.Registrations(ctx, openDay); len(left) != 0 {
		t.Errorf("the registration should be gone, got %+v", left)
	}
}

// The login page offers the guest link, so it must not offer it when guests
// have been turned off.
func TestLoginPageFollowsTheGuestSetting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if body := h.client(t).get("/login").Body.String(); !strings.Contains(body, `href="/gast"`) {
		t.Error("the guest link should be offered while guests are welcome")
	}
	if err := h.store.SaveSettings(ctx, store.Settings{
		DeadlineWeekday: time.Friday, DeadlineMinutes: 23*60 + 59,
		DeadlineWeeksBefore: 1, GuestOpen: false,
	}); err != nil {
		t.Fatal(err)
	}
	if body := h.client(t).get("/login").Body.String(); strings.Contains(body, `href="/gast"`) {
		t.Error("the login page still points at a guest page that is closed")
	}
}

// A deadline that passes with no cooking team must not be logged as if the
// list had gone out, and must not have the notifier retrying every few minutes
// for the rest of the week either.
func TestADeadlineWithNoCookingTeamIsRecordedAsUnmanned(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, id := range h.teams {
		if err := h.store.DeleteTeam(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	h.runNotifications(ctx)

	done, err := h.store.Notified(ctx, shutDay, notifyKind)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("the evening should be recorded so the notifier stops retrying")
	}
	sent, _ := h.store.SentNotifications(ctx, "2026-08-01", "2026-08-31")
	if got := sent[shutDay].Recipient; got != noTeam {
		t.Errorf("recipient = %q, want the no-team marker %q", got, noTeam)
	}

	// The admin schedule says so rather than claiming a mail went out.
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")
	body := c.get("/admin?flik=schema").Body.String()
	if !strings.Contains(body, "inget lag") {
		t.Error("the schedule should flag the evening as having had no team")
	}
}

// The rotation order is a sequence you rearrange, not a number you type, so
// two teams can never end up sharing a place.
func TestAdminReordersTheRotation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")

	order := func() []string {
		t.Helper()
		teams, err := h.store.Teams(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for i, team := range teams {
			if team.Position != i {
				t.Fatalf("%s has position %d, want %d", team.Name, team.Position, i)
			}
			out = append(out, team.Name)
		}
		return out
	}
	if got := order(); got[0] != "Lag 1" || got[1] != "Lag 2" {
		t.Fatalf("order = %v", got)
	}

	// The arrows, which is what happens without JavaScript.
	rec := c.post("/admin/lag/ordning", url.Values{"down": {itoa(int(h.teams[0]))}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("move down = %d", rec.Code)
	}
	if got := order(); got[0] != "Lag 2" || got[1] != "Lag 1" {
		t.Errorf("after moving down: %v", got)
	}
	// Nudging past the end is a no-op rather than an error.
	if rec := c.post("/admin/lag/ordning", url.Values{"down": {itoa(int(h.teams[0]))}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("move past the end = %d", rec.Code)
	}
	if got := order(); got[0] != "Lag 2" || got[1] != "Lag 1" {
		t.Errorf("moving past the end changed things: %v", got)
	}

	// The whole sequence, which is what the drag-and-drop list posts.
	rec = c.post("/admin/lag/ordning", url.Values{
		"order": {itoa(int(h.teams[0])) + "," + itoa(int(h.teams[1]))},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reorder = %d", rec.Code)
	}
	if got := order(); got[0] != "Lag 1" || got[1] != "Lag 2" {
		t.Errorf("after reordering: %v", got)
	}

	// And the schedule follows it.
	world, _ := h.world(ctx)
	first := world.Schedule.Dinners[0]
	if first.Team == nil || first.Team.Name != "Lag 1" {
		t.Errorf("first dinner is cooked by %v", first.Team)
	}
}

// A new team joins the rotation at the end, wherever the form thinks it should
// go.
func TestANewTeamJoinsLast(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")

	rec := c.post("/admin/lag", url.Values{
		"name": {"Lag 3"}, "leader_username": {"cecilia.dahl"}, "active": {"1"},
		// A stale field from an older form must not be honoured.
		"position": {"0"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d", rec.Code)
	}
	teams, _ := h.store.Teams(context.Background())
	if len(teams) != 3 || teams[2].Name != "Lag 3" {
		t.Errorf("teams = %+v", teams)
	}
}

// One diet covers a whole registration. A guest party that eats two different
// meals registers twice, which is what the form tells them to do.
func TestAGuestPartyWithTwoDietsRegistersTwice(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.client(t)

	for _, one := range []struct {
		name string
		diet store.Diet
		n    int
	}{
		{"Kalle Svensson", store.DietOmnivore, 2},
		{"Maja Öberg", store.DietVegan, 1},
	} {
		form := party(one.n, 0, one.diet, "")
		form.Set("date", openDay)
		form.Set("name", one.name)
		form.Set("host", "Anna Andersson")
		if rec := c.post("/gast", form); rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d", one.name, rec.Code)
		}
	}

	regs, _ := h.store.Registrations(ctx, openDay)
	if len(regs) != 2 {
		t.Fatalf("got %d registrations, want 2", len(regs))
	}

	sum, err := h.summary(ctx, mustFind(t, h, openDay))
	if err != nil {
		t.Fatal(err)
	}
	if sum.People != 3 {
		t.Errorf("People = %d, want 3", sum.People)
	}
	if got := sum.Count(store.DietOmnivore); got != 2 {
		t.Errorf("omnivore portions = %d, want 2", got)
	}
	if got := sum.Count(store.DietVegan); got != 1 {
		t.Errorf("vegan portions = %d, want 1", got)
	}

	// And both meals are named on the cooking team's list.
	member := h.client(t)
	member.member("Anna", "anna.andersson")
	body := member.get("/middag/" + openDay + "/lista").Body.String()
	for _, want := range []string{"Allätare", "Vegan", "Kalle Svensson", "Maja Öberg"} {
		if !strings.Contains(body, want) {
			t.Errorf("the matlista is missing %q", want)
		}
	}
}

// A household changing its mind edits one registration rather than adding one.
func TestAHouseholdHasOneDietPerDinner(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.client(t)
	c.member("Anna", "anna.andersson")

	c.post("/middag/"+openDay, party(2, 1, store.DietOmnivore, ""))
	c.post("/middag/"+openDay, party(2, 1, store.DietVegetarian, ""))

	regs, _ := h.store.Registrations(ctx, openDay)
	if len(regs) != 1 {
		t.Fatalf("got %d registrations, want 1", len(regs))
	}
	if regs[0].Diet != store.DietVegetarian {
		t.Errorf("Diet = %q, want the second answer", regs[0].Diet)
	}
	sum, _ := h.summary(ctx, mustFind(t, h, openDay))
	if sum.Count(store.DietOmnivore) != 0 || sum.Count(store.DietVegetarian) != 3 {
		t.Errorf("portions: %+v", sum.Diets)
	}
}

func mustFind(t *testing.T, h *harness, key string) dinner.Dinner {
	t.Helper()
	world, err := h.world(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d, ok := world.Schedule.Find(key)
	if !ok {
		t.Fatalf("no dinner on %s", key)
	}
	return d
}

// ------------------------------------------------------- the spreadsheet ---

// The cooking team keeps its own sheet, so the list has to come out as one
// flat table it can pull in.
func TestTheListExportsAsASpreadsheet(t *testing.T) {
	h := newHarness(t)
	anna := h.client(t)
	anna.member("Anna Andersson", "anna.andersson")
	anna.post("/middag/"+openDay, party(2, 2, store.DietVegan, "inga nötter"))
	bo := h.client(t)
	bo.member("Bo Bengtsson", "bo.bengtsson")
	bo.post("/middag/"+openDay, party(1, 0, store.DietFlexitarian, ""))

	guest := h.client(t)
	form := party(2, 0, store.DietOmnivore, "")
	form.Set("date", openDay)
	form.Set("name", "Kalle Svensson")
	form.Set("host", "Anna Andersson")
	if rec := guest.post("/gast", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("guest registration = %d", rec.Code)
	}

	rec := anna.get("/middag/" + openDay + "/lista.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "matlista-"+openDay+".csv") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	// Excel will not believe a CSV is UTF-8 without the byte-order mark, and
	// every å in the house depends on it.
	body := rec.Body.String()
	if !strings.HasPrefix(body, "\ufeff") {
		t.Error("the file should start with a byte-order mark")
	}

	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatalf("the export is not valid CSV: %v", err)
	}
	// A header and one row per household, and nothing else: no totals row and
	// no blank lines, so a formula in the sheet can rely on the shape.
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want a header and three households: %v", len(rows), rows)
	}
	width := len(rows[0])
	for i, row := range rows {
		if len(row) != width {
			t.Errorf("row %d has %d columns, want %d", i, len(row), width)
		}
	}
	if rows[0][0] != "datum" || rows[0][1] != "namn" {
		t.Errorf("header = %v", rows[0])
	}

	byName := map[string][]string{}
	for _, row := range rows[1:] {
		byName[row[1]] = row
	}
	anna_ := byName["Anna Andersson"]
	if anna_ == nil {
		t.Fatalf("Anna is missing: %v", rows)
	}
	if anna_[0] != openDay || anna_[3] != "2" || anna_[4] != "2" || anna_[5] != "4" {
		t.Errorf("Anna's row = %v", anna_)
	}
	if anna_[6] != "Vegan" {
		t.Errorf("Anna's diet = %q, want the readable name", anna_[6])
	}
	if anna_[7] != "inga nötter" {
		t.Errorf("Anna's note = %q", anna_[7])
	}
	if anna_[8] != "" {
		t.Errorf("Anna is not a guest, got %q", anna_[8])
	}

	kalle := byName["Kalle Svensson"]
	if kalle == nil {
		t.Fatal("the guest is missing from the export")
	}
	if kalle[8] != "ja" || kalle[9] != "Anna Andersson" {
		t.Errorf("the guest's row = %v", kalle)
	}
}

// A household coming on its standing registration is on the list, and marked
// so the team can see the difference.
func TestTheExportMarksStandingAndLeavesOutWhoSaidNo(t *testing.T) {
	h := newHarness(t)
	anna := h.client(t)
	anna.member("Anna", "anna.andersson")
	anna.post("/stadigvarande", withWeekday(party(2, 0, store.DietOmnivore, ""), time.Tuesday))

	bo := h.client(t)
	bo.member("Bo", "bo.bengtsson")
	bo.post("/stadigvarande", withWeekday(party(1, 0, store.DietOmnivore, ""), time.Tuesday))
	bo.post("/middag/"+openDay, url.Values{"action": {"decline"}})

	body := strings.TrimPrefix(anna.get("/middag/"+openDay+"/lista.csv").Body.String(), "\ufeff")
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want a header and Anna only: %v", len(rows), rows)
	}
	if rows[1][1] != "Anna" {
		t.Errorf("row = %v", rows[1])
	}
	if rows[1][10] != "ja" {
		t.Errorf("Anna comes on her standing registration; column = %q", rows[1][10])
	}
}

// The export is the same capability as the list, so the mailed link opens it —
// which is what lets a spreadsheet pull it in without logging in.
func TestTheExportFollowsTheSignedKey(t *testing.T) {
	h := newHarness(t)
	path := "/middag/" + openDay + "/lista.csv"
	stranger := h.client(t)

	if rec := stranger.get(path); rec.Code != http.StatusSeeOther {
		t.Errorf("without a key = %d, want a redirect to the login page", rec.Code)
	}
	key := h.guard.Key(listKeyPurpose, openDay, time.Hour)
	if rec := stranger.get(path + "?nyckel=" + url.QueryEscape(key)); rec.Code != http.StatusOK {
		t.Errorf("with the mailed key = %d, want 200", rec.Code)
	}
	// And nothing else.
	if rec := stranger.get("/middag/" + laterDay + "/lista.csv?nyckel=" + url.QueryEscape(key)); rec.Code != http.StatusSeeOther {
		t.Errorf("the key opened another evening's export: %d", rec.Code)
	}
	if rec := stranger.get(path + "?nyckel=forged"); rec.Code != http.StatusSeeOther {
		t.Errorf("a forged key was accepted: %d", rec.Code)
	}
}

// The list page offers the download to anybody who may read it, but the link
// that works without a password only to the team and the administrator.
func TestTheLiveFormulaIsOnlyOfferedToTheTeam(t *testing.T) {
	h := newHarness(t)

	member := h.client(t)
	member.member("Anna", "anna.andersson")
	body := member.get("/middag/" + openDay + "/lista").Body.String()
	if !strings.Contains(body, "lista.csv") {
		t.Error("a member should be offered the download")
	}
	if strings.Contains(body, "IMPORTDATA") {
		t.Error("a member should not be handed a password-free link to share")
	}

	admin := h.client(t)
	admin.login("adm")
	admin.identify("Chef", "cecilia.dahl")
	body = admin.get("/middag/" + openDay + "/lista").Body.String()
	if !strings.Contains(body, "IMPORTDATA") {
		t.Error("the administrator should get the live formula")
	}
	if !strings.Contains(body, "https://mat.example.se/middag/"+openDay+"/lista.csv?nyckel=") {
		t.Error("the formula needs an absolute, keyed address")
	}

	// The leader who followed the mailed link gets it too.
	key := h.guard.Key(listKeyPurpose, openDay, time.Hour)
	leader := h.client(t)
	body = leader.get("/middag/" + openDay + "/lista?nyckel=" + url.QueryEscape(key)).Body.String()
	if !strings.Contains(body, "IMPORTDATA") {
		t.Error("the cooking team should get the live formula")
	}
	// Their download link has to keep the key, or it just bounces to login.
	if !strings.Contains(body, "lista.csv?nyckel=") {
		t.Error("the download link should carry the reader's key")
	}
}

// Dvir asked whether a late registration would be possible. It must not be,
// in any of the ways somebody might try.
func TestRegistrationIsImpossibleAfterTheDeadline(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	member := h.client(t)
	member.member("Anna", "anna.andersson")
	if rec := member.post("/middag/"+shutDay, party(2, 0, store.DietOmnivore, "")); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a member registering late = %d, want 422", rec.Code)
	}

	// A guest cannot pick a closed evening, and it is not even offered.
	guest := h.client(t)
	if body := guest.get("/gast").Body.String(); strings.Contains(body, `value="`+shutDay+`"`) {
		t.Error("a closed evening should not be offered on the guest page")
	}
	form := party(1, 0, store.DietOmnivore, "")
	form.Set("date", shutDay)
	form.Set("name", "Kalle")
	if rec := guest.post("/gast", form); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a guest registering late = %d, want 422", rec.Code)
	}

	// Nor by coming back to an existing guest registration afterwards. Make one
	// for an open evening, then move the deadline past it.
	form = party(1, 0, store.DietOmnivore, "")
	form.Set("date", openDay)
	form.Set("name", "Maja")
	rec := guest.post("/gast", form)
	link := strings.Split(rec.Header().Get("Location"), "?")[0]
	if err := h.store.SaveSettings(ctx, store.Settings{
		DeadlineWeekday: time.Friday, DeadlineMinutes: 23*60 + 59,
		DeadlineWeeksBefore: 4, GuestOpen: true,
	}); err != nil {
		t.Fatal(err)
	}
	if rec := guest.post(link, party(9, 9, store.DietOmnivore, "")); rec.Code != http.StatusConflict {
		t.Errorf("changing a guest registration after the deadline = %d, want 409", rec.Code)
	}
	regs, _ := h.store.Registrations(ctx, openDay)
	if len(regs) != 1 || regs[0].Adults != 1 {
		t.Errorf("the registration was changed anyway: %+v", regs)
	}
}

// The admin view is where the guest link and the spreadsheet formula are read
// off the screen, so it has to say when the address in them is not real.
func TestAdminWarnsWhenTheSiteAddressIsUnset(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")

	// The harness is configured with a real address, so no warning.
	if body := c.get("/admin").Body.String(); strings.Contains(body, "BASE_URL") {
		t.Error("a configured address should not be warned about")
	}

	h.rt.BaseURL = config.DefaultBaseURL
	body := c.get("/admin").Body.String()
	if !strings.Contains(body, "BASE_URL") {
		t.Error("an unset address should be warned about")
	}
	if !strings.Contains(body, config.DefaultBaseURL) {
		t.Error("the warning should say what the address currently is")
	}

	// The demo runs on localhost on purpose and should not nag about it.
	h.rt.Demo = true
	if body := c.get("/admin").Body.String(); strings.Contains(body, "BASE_URL") {
		t.Error("the demo should not warn about its own address")
	}
}

// ------------------------------------------ the confirmation and the calendar --

// Registering is confirmed where the household already reads: a direct message
// from the bot, with the evening attached for whatever calendar they keep. It
// is what replaced the confirmation e-mail, so there is no address anywhere in
// it.
func TestRegisteringIsConfirmedInMattermost(t *testing.T) {
	h := newChatHarness(t)
	c := h.client(t)
	c.member("Anna Andersson", "anna.andersson")

	if rec := c.post("/middag/"+openDay, party(2, 1, store.DietVegetarian, "glutenfritt")); rec.Code != http.StatusSeeOther {
		t.Fatalf("register = %d — %s", rec.Code, rec.Body.String())
	}

	dm := h.chat.waitForDMTo(t, "anna.andersson", 1)
	for _, want := range []string{
		"Hej Anna!",
		"tisdag 25 augusti",
		"| **Antal** | 2 vuxna, 1 barn |",
		"| **Kosthållning** | Vegetarian |",
		"| **Allergier** | glutenfritt |",
		"| **Var** | stora matsalen |",
		"https://mat.example.se/middag/" + openDay,
	} {
		if !strings.Contains(dm.Message, want) {
			t.Errorf("the confirmation is missing %q:\n%s", want, dm.Message)
		}
	}
	// The evening itself comes as a calendar file, so it can be kept without
	// typing anything anywhere.
	ics, ok := dm.Files["middag-"+openDay+".ics"]
	if !ok || len(dm.Files) != 1 {
		t.Fatalf("attachments = %v, want one calendar file", dm.Files)
	}
	if !strings.Contains(ics, "STATUS:CONFIRMED") || !strings.Contains(ics, "DTSTART:20260825T160000Z") {
		t.Errorf("the attached evening reads:\n%s", ics)
	}

	// Changing the answer confirms the new one rather than the old.
	c.post("/middag/"+openDay, party(1, 0, store.DietVegan, ""))
	second := h.chat.waitForDMTo(t, "anna.andersson", 2)
	if !strings.Contains(second.Message, "| **Antal** | 1 vuxen |") {
		t.Errorf("the second confirmation should carry the new numbers:\n%s", second.Message)
	}
}

// Saying "we are not coming" is an answer too, and it is confirmed as one —
// with a cancellation for a calendar that already held the evening.
func TestDecliningIsConfirmedAsNotComing(t *testing.T) {
	h := newChatHarness(t)
	c := h.client(t)
	c.member("Anna Andersson", "anna.andersson")
	form := party(0, 0, store.DietOmnivore, "")
	form.Set("action", "decline")
	c.post("/middag/"+openDay, form)

	dm := h.chat.waitForDMTo(t, "anna.andersson", 1)
	if !strings.Contains(dm.Message, "inte med") {
		t.Errorf("the message should say they are not coming:\n%s", dm.Message)
	}
	if strings.Contains(dm.Message, "**Antal**") {
		t.Errorf("there is nobody to count:\n%s", dm.Message)
	}
	// The evening still comes along, as a cancellation: a calendar that
	// already held it should let go of it.
	ics, ok := dm.Files["middag-"+openDay+".ics"]
	if !ok {
		t.Fatalf("attachments = %v, want the evening as a cancellation", dm.Files)
	}
	if !strings.Contains(ics, "STATUS:CANCELLED") {
		t.Errorf("the attached evening should be cancelled:\n%s", ics)
	}
}

// Without a chat server there is nobody to confirm to. The registration is
// saved all the same, and the page says as much rather than claiming a message
// went out.
func TestWithoutMattermostRegisteringStillWorks(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna Andersson", "anna.andersson")
	if rec := c.post("/middag/"+openDay, party(2, 0, store.DietOmnivore, "")); rec.Code != http.StatusSeeOther {
		t.Fatalf("register = %d", rec.Code)
	}
	if _, err := h.store.MemberRegistration(context.Background(), openDay, "anna.andersson"); err != nil {
		t.Fatalf("the registration should be stored anyway: %v", err)
	}
	if body := c.get("/middag/" + openDay + "?sparat=1").Body.String(); strings.Contains(body, "direktmeddelande") {
		t.Error("the page should not promise a message it cannot send")
	}
}

// A household that is coming is offered the evening for its own calendar, and
// the file is a calendar a calendar can read.
func TestTheEveningCanBeAddedToTheCalendar(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna Andersson", "anna.andersson")
	c.post("/middag/"+openDay, party(2, 0, store.DietOmnivore, ""))

	body := c.get("/middag/" + openDay).Body.String()
	for _, want := range []string{
		"/middag/" + openDay + "/kalender.ics",
		"calendar.google.com",
		"outlook.office.com",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page should offer %q", want)
		}
	}

	rec := c.get("/middag/" + openDay + "/kalender.ics")
	if rec.Code != http.StatusOK {
		t.Fatalf("the calendar file = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/calendar") {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "middag-"+openDay+".ics") {
		t.Errorf("Content-Disposition = %q", got)
	}
	ics := rec.Body.String()
	for _, want := range []string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT", "END:VCALENDAR",
		// 18:00 in Stockholm on a summer evening is 16:00 UTC.
		"DTSTART:20260825T160000Z",
		"DTEND:20260825T173000Z",
		"STATUS:CONFIRMED",
		"X-WR-TIMEZONE:Europe/Stockholm",
	} {
		if !strings.Contains(ics, want) {
			t.Errorf("the calendar file is missing %q:\n%s", want, ics)
		}
	}
}

// A guest leaves no address of any kind. The page is the confirmation, and it
// hands the evening to their calendar the same way.
func TestAGuestNeedsNoAddressAndGetsAConfirmation(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)

	// There is nowhere to type an address, because nothing is ever sent.
	if body := c.get("/gast").Body.String(); strings.Contains(body, `type="email"`) {
		t.Error("the guest form still asks for an e-mail address")
	}

	form := party(2, 0, store.DietPescetarian, "skaldjursallergi")
	form.Set("date", openDay)
	form.Set("name", "Kalle Svensson")
	form.Set("host", "Anna Andersson")
	rec := c.post("/gast", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("guest registration = %d — %s", rec.Code, rec.Body.String())
	}
	link := rec.Header().Get("Location")

	body := c.get(link).Body.String()
	for _, want := range []string{
		"Kalle Svensson", // the receipt says what was registered
		"Pescetarian",
		"kalender.ics", // and offers it to their calendar
		"calendar.google.com",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the guest's confirmation is missing %q", want)
		}
	}

	token := strings.TrimPrefix(strings.Split(link, "?")[0], "/gast/")
	ics := c.get("/gast/" + token + "/kalender.ics")
	if ics.Code != http.StatusOK {
		t.Fatalf("the guest's calendar file = %d", ics.Code)
	}
	// It points back at the guest's own link, not at a page they cannot open,
	// and says nothing about the house.
	if !strings.Contains(ics.Body.String(), "/gast/"+token) {
		t.Errorf("the file should link back to the guest's own page:\n%s", ics.Body.String())
	}
	if strings.Contains(ics.Body.String(), "Matlag") {
		t.Errorf("a guest is not told who cooks:\n%s", ics.Body.String())
	}

	// A token nobody has is nothing at all.
	if rec := c.get("/gast/hittepa/kalender.ics"); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown token = %d, want 404", rec.Code)
	}
}

// ------------------------------------------------------- saying who you are --

// The field takes what somebody is likely to have: their username, a pasted
// mention, or their name. Two people with the same name is a choice the site
// must not make for them.
func TestAHouseholdSaysWhoItIsByAccount(t *testing.T) {
	for _, tc := range []struct {
		name, typed, want string
		status            int
	}{
		{"a username", "cecilia.dahl", "cecilia.dahl", http.StatusSeeOther},
		{"a pasted mention", "@cecilia.dahl", "cecilia.dahl", http.StatusSeeOther},
		{"a full name", "Cecilia Dahl", "cecilia.dahl", http.StatusSeeOther},
		{"a folded name", "cecilia dahl", "cecilia.dahl", http.StatusSeeOther},
		{"nobody at all", "", "", http.StatusUnprocessableEntity},
		{"a stranger", "hittepa.person", "", http.StatusUnprocessableEntity},
		{"two people at once", "Anna Andersson", "", http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newChatHarness(t)
			c := h.client(t)
			c.login("hus")
			rec := c.post("/jagar", url.Values{"member": {tc.typed}, "next": {"/"}})
			if rec.Code != tc.status {
				t.Fatalf("= %d, want %d — %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status != http.StatusSeeOther {
				// The reason is on the page, not in a log somewhere.
				if body := rec.Body.String(); !strings.Contains(body, "Vem är du?") {
					t.Errorf("the form should come back with the problem:\n%s", body)
				}
				return
			}
			// Registering now stores that account, and the name comes from it.
			c.post("/middag/"+openDay, party(1, 0, store.DietOmnivore, ""))
			got, err := h.store.MemberRegistration(context.Background(), openDay, tc.want)
			if err != nil {
				t.Fatalf("stored registration: %v", err)
			}
			if got.Name != "Cecilia Dahl" {
				t.Errorf("Name = %q, want the name from the account", got.Name)
			}
			if got.MMUserID == "" {
				t.Error("the account id is what the confirmation is sent to; it must be stored")
			}
		})
	}
}

// Two people share a name, so the page says which two rather than guessing.
func TestAnAmbiguousNameNamesTheCandidates(t *testing.T) {
	h := newChatHarness(t)
	c := h.client(t)
	c.login("hus")
	rec := c.post("/jagar", url.Values{"member": {"Anna Andersson"}, "next": {"/"}})
	body := rec.Body.String()
	for _, want := range []string{"anna.andersson", "anna.a"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page should name @%s as one of the candidates:\n%s", want, body)
		}
	}
}

// A name written on the form wins over the one on the account: somebody may go
// by something else in the house than in the chat.
func TestATypedNameWinsOverTheAccountsName(t *testing.T) {
	h := newChatHarness(t)
	c := h.client(t)
	c.login("hus")
	c.identify("Cissi och Dan", "cecilia.dahl")
	c.post("/middag/"+openDay, party(2, 0, store.DietOmnivore, ""))
	got, err := h.store.MemberRegistration(context.Background(), openDay, "cecilia.dahl")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Cissi och Dan" {
		t.Errorf("Name = %q, want what the household wrote", got.Name)
	}
}

// The confirmation follows the language the household has set in Mattermost,
// not the one the site happens to be showing: the message turns up in the
// chat, on the chat's terms.
func TestTheConfirmationFollowsTheChatsLanguage(t *testing.T) {
	h := newChatHarness(t)
	bo := h.client(t)
	bo.member("Bo Bengtsson", "bo.bengtsson")
	bo.post("/middag/"+openDay, party(1, 0, store.DietVegan, ""))

	dm := h.chat.waitForDMTo(t, "bo.bengtsson", 1)
	if !strings.Contains(dm.Message, "Hi Bo!") {
		t.Errorf("Bo reads English in the chat:\n%s", dm.Message)
	}

	// Anna has set nothing, so she gets the house's own language.
	anna := h.client(t)
	anna.member("Anna Andersson", "anna.andersson")
	anna.post("/middag/"+openDay, party(1, 0, store.DietOmnivore, ""))
	if got := h.chat.waitForDMTo(t, "anna.andersson", 1); !strings.Contains(got.Message, "Hej Anna!") {
		t.Errorf("Anna should get the house's language:\n%s", got.Message)
	}
}

// --- the picker when the directory is out of reach ---------------------------

// Listing every account in a Mattermost needs rights a bot token is not
// usually given; searching needs none. The picker used to fetch the list, get
// a 502 back from our own server, catch it and offer nothing — a field that
// looked simply broken, which is what it was reported as. It has to say what
// it cannot do, and leave the browser a way through.
func TestThePickerFallsBackWhenTheDirectoryIsForbidden(t *testing.T) {
	h := newChatHarness(t)
	h.chat.mu.Lock()
	h.chat.forbidDirectory = true
	h.chat.mu.Unlock()

	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")

	rec := c.get("/medlemmar")
	if rec.Code != http.StatusOK {
		t.Fatalf("a directory we may not read should still answer the picker, got %d", rec.Code)
	}
	var got memberList
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — %s", err, rec.Body.String())
	}
	if !got.AskServer {
		t.Error("the browser was not told to let the server search")
	}
	if !got.Unreachable {
		t.Error("a failed lookup was reported as an empty house")
	}

	// And the route it was pointed at works: searching asks Mattermost, which
	// a bot is allowed to do.
	rec = c.get("/medlemmar?q=ostberg")
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d", rec.Code)
	}
	got = memberList{}
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Users) != 1 || got.Users[0].Username != "mikael.ostberg" {
		t.Fatalf("search offered %+v", got.Users)
	}
	if got.Unreachable {
		t.Error("a search that worked was reported as unreachable")
	}
}

// --- saving in the admin view -----------------------------------------------

// Every save used to land on the first tab, because the hidden field it
// redirected through named a URL with no tab in it. Worse, the "sparat" query
// was appended after a "#lag" fragment, where the browser never sends it — so
// the confirmation did not show either.
func TestSavingStaysOnTheTabItWasMadeOn(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.login("adm")

	for _, tc := range []struct {
		name string
		path string
		form url.Values
	}{
		{"teams", "/admin/lag", url.Values{
			"flik": {"lag"}, "name": {"Lag 9"}, "active": {"1"}}},
		{"order", "/admin/lag/ordning", url.Values{
			"flik": {"lag"}, "down": {itoa(int(h.teams[0]))}}},
		{"breaks", "/admin/uppehall", url.Values{
			"flik": {"uppehall"}, "name": {"Jul"},
			"start": {"2026-12-21"}, "end": {"2027-01-06"}}},
		{"settings", "/admin/installningar", url.Values{
			"flik": {"installningar"}, "deadline_weekday": {"3"},
			"deadline_time": {"18:00"}, "deadline_weeks_before": {"1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := c.post(tc.path, tc.form)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("%s = %d — %s", tc.path, rec.Code, rec.Body.String())
			}
			loc, err := url.Parse(rec.Header().Get("Location"))
			if err != nil {
				t.Fatalf("Location: %v", err)
			}
			want := tc.form.Get("flik")
			if got := loc.Query().Get("flik"); got != want {
				t.Errorf("came back to tab %q, wanted %q (%s)", got, want, loc)
			}
			// The confirmation has to survive the trip too, which means the
			// query cannot be hidden behind the fragment.
			if loc.Query().Get("sparat") == "" {
				t.Errorf("nothing to confirm the save: %s", loc)
			}
		})
	}
}

// The schedule is shown one season at a time, so a save there has to come back
// to the season it was made in.
func TestSavingAnEveningKeepsTheSeasonInView(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.login("adm")

	seasons, _ := h.store.Seasons(context.Background())
	if len(seasons) == 0 {
		t.Fatal("no season to save in")
	}
	season := itoa(int(seasons[0].ID))

	rec := c.post("/admin/schema", url.Values{
		"flik": {"schema"}, "sasong": {season},
		"date": {openDay}, "note": {"Soppa"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d — %s", rec.Code, rec.Body.String())
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if got := loc.Query().Get("sasong"); got != season {
		t.Errorf("came back to season %q, wanted %q (%s)", got, season, loc)
	}
	if got := loc.Query().Get("flik"); got != "schema" {
		t.Errorf("came back to tab %q (%s)", got, loc)
	}
}

// Setting a season up means naming several teams and their leaders. Saving one
// row at a time was a page load and a lost scroll position per team, so the
// whole list saves at once.
func TestEveryTeamSavesAtOnce(t *testing.T) {
	h := newChatHarness(t)
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")

	teams, _ := h.store.Teams(context.Background())
	if len(teams) < 2 {
		t.Fatalf("need at least two teams to save together, have %d", len(teams))
	}
	first, second := teams[0], teams[1]

	form := url.Values{"flik": {"lag"}}
	form.Add("id", itoa(int(first.ID)))
	form.Add("id", itoa(int(second.ID)))
	form.Set("name_"+itoa(int(first.ID)), "Kokerskorna")
	form.Set("leader_"+itoa(int(first.ID)), "anna.a")
	form.Set("active_"+itoa(int(first.ID)), "1")
	form.Set("name_"+itoa(int(second.ID)), "Grytan")
	form.Set("leader_"+itoa(int(second.ID)), "Mikael Östberg")
	// The second team is left out of the rotation, which a missing checkbox is
	// how a browser says.

	rec := c.post("/admin/lag/alla", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save all = %d — %s", rec.Code, rec.Body.String())
	}

	after, _ := h.store.Teams(context.Background())
	byID := map[int64]store.Team{}
	for _, t := range after {
		byID[t.ID] = t
	}
	if got := byID[first.ID]; got.Name != "Kokerskorna" ||
		got.LeaderUsername != "anna.a" || !got.Active {
		t.Errorf("the first team = %+v", got)
	}
	// A leader named rather than typed still resolves to the account, and the
	// name comes back from it.
	if got := byID[second.ID]; got.Name != "Grytan" ||
		got.LeaderUsername != "mikael.ostberg" ||
		got.LeaderName != "Mikael Östberg" || got.Active {
		t.Errorf("the second team = %+v", got)
	}
}

// A leader who is a typo must not take the rest of the list down with it, and
// must not be saved half-way either: nothing changes until every row can.
func TestOneBadLeaderRefusesTheWholeSave(t *testing.T) {
	h := newChatHarness(t)
	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")

	teams, _ := h.store.Teams(context.Background())
	first, second := teams[0], teams[1]

	form := url.Values{"flik": {"lag"}}
	form.Add("id", itoa(int(first.ID)))
	form.Add("id", itoa(int(second.ID)))
	form.Set("name_"+itoa(int(first.ID)), "Nytt namn")
	form.Set("leader_"+itoa(int(first.ID)), "anna.a")
	form.Set("name_"+itoa(int(second.ID)), "Grytan")
	form.Set("leader_"+itoa(int(second.ID)), "ingen.som.finns")

	rec := c.post("/admin/lag/alla", form)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("save all = %d, wanted it refused", rec.Code)
	}
	// The team the reader has to go and fix is named, which is the whole point
	// of reporting the row rather than the field.
	if body := rec.Body.String(); !strings.Contains(body, second.Name) {
		t.Errorf("the refusal does not say which team: %s", body)
	}

	after, _ := h.store.Teams(context.Background())
	if after[0].Name != first.Name {
		t.Errorf("the good row was saved anyway: %+v", after[0])
	}
}

// --- the stylesheet and the scripts -----------------------------------------

// An upgrade changes app.css without changing its address, so a browser that
// cached the previous one keeps it — which is how an upgraded site rendered
// with the stylesheet of the version before it, putting the @ of the
// Mattermost field above its box instead of inside it. Every asset is
// addressed by its contents now, so a new build is an address no cache can
// answer from.
func TestTheStylesheetIsAddressedByItsContents(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)

	// The login page, because it is the one page a browser reaches before it
	// has anything else — and the first chance to hand it a stale stylesheet.
	body := c.get("/login").Body.String()
	for _, file := range []string{"app.css", "app.js", "members.js"} {
		if !strings.Contains(body, "/static/"+file+"?v=") {
			t.Errorf("%s is linked without a version: a cache will keep the old one", file)
		}
	}

	// A versioned address may be kept forever, because its contents cannot
	// change without the address changing.
	versioned := c.get("/static/app.css?v=" + h.assets["app.css"])
	if versioned.Code != http.StatusOK {
		t.Fatalf("versioned stylesheet = %d", versioned.Code)
	}
	if cc := versioned.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned Cache-Control = %q", cc)
	}

	// The bare address might be a previous build's, so it gets a short life.
	bare := c.get("/static/app.css")
	if cc := bare.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=60") {
		t.Errorf("unversioned Cache-Control = %q, wanted a short one", cc)
	}
}

// Editing a static file has to change its address, or the fingerprinting is
// decoration.
func TestEachAssetHasItsOwnVersion(t *testing.T) {
	h := newHarness(t)
	if len(h.assets) == 0 {
		t.Fatal("no assets were fingerprinted")
	}
	if h.assets["app.css"] == h.assets["app.js"] {
		t.Errorf("two different files share a version: %q", h.assets["app.css"])
	}
	for name, version := range h.assets {
		if len(version) != 8 {
			t.Errorf("%s has version %q", name, version)
		}
	}
}

// --- the deadline, and the way round it -------------------------------------

// openThursday is the next Thursday whose deadline has not passed: it closes
// on Friday 21 August, the day after these tests think it is. laterDay is a
// Tuesday, so a Thursday standing registration says nothing about it.
const openThursday = "2026-08-27"

// Registering for a single evening is refused once the deadline has passed.
// Saving a standing registration for that weekday used to be a way straight
// past it: the standing registrations are read live when a list is drawn up,
// so a new one appeared on an evening the cooking team had already been given
// and shopped for.
func TestAStandingRegistrationCannotBeUsedToBeatTheDeadline(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna Andersson", "anna.andersson")

	// The direct road is shut, which is the behaviour the other one has to
	// match.
	if rec := c.post("/middag/"+shutDay, party(2, 0, store.DietOmnivore, "")); rec.Code == http.StatusSeeOther {
		t.Fatal("registering for a closed evening was accepted")
	}

	// shutDay is a Thursday still to come whose deadline passed a week ago.
	form := party(2, 0, store.DietOmnivore, "")
	form.Set("weekday", itoa(int(time.Thursday)))
	if rec := c.post("/stadigvarande", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving the standing registration = %d — %s", rec.Code, rec.Body.String())
	}

	if list := c.get("/middag/" + shutDay + "/lista").Body.String(); strings.Contains(list, "Anna") {
		t.Errorf("a standing registration saved after the deadline reached %s's list", shutDay)
	}

	// It still does its job for the evenings it was in time for.
	if list := c.get("/middag/" + openThursday + "/lista").Body.String(); !strings.Contains(list, "Anna") {
		t.Errorf("the standing registration did not reach %s, which is still open", openThursday)
	}
}

// The fix must not overshoot. A household that has been eating every Thursday
// for a year is already on tonight's list; editing the standing registration
// after tonight's deadline must not take them off it, because the cooking team
// has already shopped for them.
func TestEditingAStandingRegistrationLeavesAClosedEveningAsItWasSent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.client(t)
	c.member("Anna Andersson", "anna.andersson")

	// In force since long before shutDay's deadline.
	if err := h.store.SaveStanding(ctx, store.Standing{
		ID: auth.ID(), Member: "anna.andersson", Weekday: time.Thursday,
		Name: "Anna Andersson", Adults: 2, Diet: store.DietOmnivore,
		UpdatedAt: testNow.AddDate(0, -6, 0),
	}); err != nil {
		t.Fatal(err)
	}
	before, err := h.summary(ctx, mustDinner(t, h, shutDay))
	if err != nil {
		t.Fatal(err)
	}
	if before.People != 2 {
		t.Fatalf("the household was not counted on %s to begin with: %d", shutDay, before.People)
	}

	// Now change it, after that evening's deadline.
	form := party(5, 0, store.DietVegan, "")
	form.Set("weekday", itoa(int(time.Thursday)))
	if rec := c.post("/stadigvarande", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving = %d — %s", rec.Code, rec.Body.String())
	}

	after, err := h.summary(ctx, mustDinner(t, h, shutDay))
	if err != nil {
		t.Fatal(err)
	}
	if after.People != 2 {
		t.Errorf("%s now counts %d people; it was sent with 2", shutDay, after.People)
	}
	if got := after.Count(store.DietVegan); got != 0 {
		t.Errorf("%s picked up %d vegan portions from an edit made after its deadline", shutDay, got)
	}

	// The new numbers do apply to the evenings that are still open.
	open, err := h.summary(ctx, mustDinner(t, h, openThursday))
	if err != nil {
		t.Fatal(err)
	}
	if open.People != 5 {
		t.Errorf("%s counts %d people, want the new 5", openThursday, open.People)
	}
}

// Clearing a standing registration is the same story: it must not remove a
// household from an evening it is already counted on.
func TestClearingAStandingRegistrationLeavesAClosedEveningAlone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.client(t)
	c.member("Anna Andersson", "anna.andersson")

	if err := h.store.SaveStanding(ctx, store.Standing{
		ID: auth.ID(), Member: "anna.andersson", Weekday: time.Thursday,
		Name: "Anna Andersson", Adults: 2, Diet: store.DietOmnivore,
		UpdatedAt: testNow.AddDate(0, -6, 0),
	}); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"weekday": {itoa(int(time.Thursday))}, "action": {"clear"}}
	if rec := c.post("/stadigvarande", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("clearing = %d — %s", rec.Code, rec.Body.String())
	}

	shut, err := h.summary(ctx, mustDinner(t, h, shutDay))
	if err != nil {
		t.Fatal(err)
	}
	if shut.People != 2 {
		t.Errorf("%s counts %d people after the standing registration was cleared; "+
			"the team was given 2", shutDay, shut.People)
	}
	// And it really is gone from the evenings still open.
	open, err := h.summary(ctx, mustDinner(t, h, openThursday))
	if err != nil {
		t.Fatal(err)
	}
	if open.People != 0 {
		t.Errorf("%s still counts %d people", openThursday, open.People)
	}
}

// An answer the household gave for the date itself is its own word on the
// evening, and must survive an edit to the standing registration untouched.
func TestAnAnswerForTheDateSurvivesAStandingEdit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.client(t)
	c.member("Anna Andersson", "anna.andersson")

	if err := h.store.SaveStanding(ctx, store.Standing{
		ID: auth.ID(), Member: "anna.andersson", Weekday: time.Thursday,
		Name: "Anna Andersson", Adults: 2, Diet: store.DietOmnivore,
		UpdatedAt: testNow.AddDate(0, -6, 0),
	}); err != nil {
		t.Fatal(err)
	}
	// Before that Thursday shut, the household said it was skipping it.
	if err := h.store.SaveRegistration(ctx, store.Registration{
		ID: auth.ID(), Date: shutDay, Kind: store.KindMember,
		Member: "anna.andersson", Name: "Anna Andersson",
		Adults: 0, Children: 0, Diet: store.DietOmnivore,
		CreatedAt: testNow.AddDate(0, 0, -14), UpdatedAt: testNow.AddDate(0, 0, -14),
	}); err != nil {
		t.Fatal(err)
	}

	form := party(5, 0, store.DietOmnivore, "")
	form.Set("weekday", itoa(int(time.Thursday)))
	if rec := c.post("/stadigvarande", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving = %d", rec.Code)
	}

	sum, err := h.summary(ctx, mustDinner(t, h, shutDay))
	if err != nil {
		t.Fatal(err)
	}
	if sum.People != 0 {
		t.Errorf("%s counts %d people; the household had said it was not coming",
			shutDay, sum.People)
	}
}

// mustDinner finds one evening in the generated schedule.
func mustDinner(t *testing.T, h *harness, key string) dinner.Dinner {
	t.Helper()
	world, err := h.world(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	d, ok := world.Schedule.Find(key)
	if !ok {
		t.Fatalf("no dinner on %s", key)
	}
	return d
}

// A household that saves a standing registration after tonight's deadline is
// not counted for tonight, and the page has to say which evening it does
// reach — otherwise the rule just looks like the site ignoring them.
func TestTheStandingFormSaysWhichEveningAChangeReaches(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna Andersson", "anna.andersson")

	body := c.get("/mina").Body.String()
	// Thursday 20 August has closed, so a change to the Thursday default
	// reaches the 27th.
	if !strings.Contains(body, "27 augusti") {
		t.Errorf("the standing form does not name the evening a change applies from:\n%s", body)
	}
}
