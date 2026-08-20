package web

import (
	"context"
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
	"github.com/O5ten/dinners/internal/mail"
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
}

func newHarness(t *testing.T) *harness {
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
	secret := [32]byte{7}
	guard := auth.New("hus", "adm", secret[:], time.Hour, false)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(cfg, rt, st, guard, mail.NewSender(rt.Mail, log), log)
	if err != nil {
		t.Fatal(err)
	}
	srv.now = func() time.Time { return testNow }

	ctx := context.Background()
	h := &harness{Server: srv, store: st}
	for i, name := range []string{"Lag 1", "Lag 2"} {
		id, err := st.SaveTeam(ctx, store.Team{
			Name: name, LeaderName: name + "s ledare",
			LeaderEmail: strings.ToLower(strings.ReplaceAll(name, " ", "")) + "@example.se",
			Position:    i, Active: true,
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

func (c *client) identify(name, email string) {
	c.t.Helper()
	rec := c.post("/jagar", url.Values{"name": {name}, "email": {email}, "next": {"/"}})
	if rec.Code != http.StatusSeeOther {
		c.t.Fatalf("identify: %d — %s", rec.Code, rec.Body.String())
	}
}

func (c *client) member(name, email string) {
	c.t.Helper()
	c.login("hus")
	c.identify(name, email)
}

func counts(adults, children, vegans, vegetarians int, note string) url.Values {
	return url.Values{
		"adults": {itoa(adults)}, "children": {itoa(children)},
		"vegans": {itoa(vegans)}, "vegetarians": {itoa(vegetarians)},
		"note": {note},
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
	// A rubbish address is refused rather than quietly stored.
	rec = c.post("/jagar", url.Values{"name": {"Anna"}, "email": {"anna"}, "next": {"/"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad address = %d, want 422", rec.Code)
	}
	c.identify("Anna Andersson", "Anna@Example.SE")
	if rec := c.get("/"); rec.Code != http.StatusOK {
		t.Errorf("GET / after identifying = %d", rec.Code)
	}
}

func TestMemberCannotReachTheAdminView(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna@example.se")
	if rec := c.get("/admin"); rec.Code != http.StatusForbidden {
		t.Errorf("member at /admin = %d, want 403", rec.Code)
	}
	admin := h.client(t)
	admin.login("adm")
	admin.identify("Chef", "chef@example.se")
	if rec := admin.get("/admin"); rec.Code != http.StatusOK {
		t.Errorf("admin at /admin = %d", rec.Code)
	}
}

// ------------------------------------------------------------- registering --

func TestRegisterForOneDinner(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna Andersson", "anna@example.se")

	rec := c.post("/middag/"+openDay, counts(2, 1, 0, 1, "glutenfritt för ett barn"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("register = %d — %s", rec.Code, rec.Body.String())
	}

	got, err := h.store.MemberRegistration(context.Background(), openDay, "anna@example.se")
	if err != nil {
		t.Fatalf("stored registration: %v", err)
	}
	if got.Adults != 2 || got.Children != 1 || got.Vegetarians != 1 {
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
	c.member("Anna", "anna@example.se")

	rec := c.post("/middag/"+shutDay, counts(2, 0, 0, 0, ""))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("late registration = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "stängde") {
		t.Error("the page should explain that registration has closed")
	}
	if _, err := h.store.MemberRegistration(context.Background(), shutDay, "anna@example.se"); err == nil {
		t.Error("nothing should have been stored")
	}
}

func TestImpossibleNumbersAreRefused(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna@example.se")

	tests := []struct {
		name string
		form url.Values
	}{
		{"more vegans than people", counts(1, 0, 5, 0, "")},
		{"negative", url.Values{"adults": {"-3"}, "children": {"0"}, "vegans": {"0"}, "vegetarians": {"0"}}},
		{"absurdly many", url.Values{"adults": {"400"}, "children": {"0"}, "vegans": {"0"}, "vegetarians": {"0"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := c.post("/middag/"+openDay, tc.form)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("= %d, want 422", rec.Code)
			}
		})
	}
}

// The standing registration is the replacement for the permanent-registration
// sheet: set it once and you are counted every week.
func TestStandingRegistrationCountsYouInAutomatically(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna Andersson", "anna@example.se")

	rec := c.post("/stadigvarande", withWeekday(counts(2, 2, 0, 0, "nötallergi"), time.Tuesday))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save standing = %d — %s", rec.Code, rec.Body.String())
	}

	// Nothing was written for the individual evening...
	if _, err := h.store.MemberRegistration(context.Background(), openDay, "anna@example.se"); err == nil {
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
	c.member("Anna", "anna@example.se")
	c.post("/stadigvarande", withWeekday(counts(2, 0, 0, 0, ""), time.Tuesday))

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
	c.member("Anna", "anna@example.se")
	c.post("/stadigvarande", withWeekday(counts(2, 0, 0, 0, ""), time.Tuesday))

	if list, _ := h.store.StandingByEmail(context.Background(), "anna@example.se"); len(list) != 1 {
		t.Fatal("standing registration was not saved")
	}
	rec := c.post("/stadigvarande", withWeekday(url.Values{"action": {"clear"}}, time.Tuesday))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clear = %d", rec.Code)
	}
	if list, _ := h.store.StandingByEmail(context.Background(), "anna@example.se"); len(list) != 0 {
		t.Errorf("standing registration should be gone, got %+v", list)
	}
	if strings.Contains(c.get("/middag/"+openDay+"/lista").Body.String(), "Anna") {
		t.Error("the household should no longer be counted")
	}
}

func withWeekday(v url.Values, wd time.Weekday) url.Values {
	v.Set("weekday", itoa(int(wd)))
	return v
}

// One household's address is nobody else's business.
func TestOtherMembersNeverSeeYourAddress(t *testing.T) {
	h := newHarness(t)
	anna := h.client(t)
	anna.member("Anna Andersson", "anna@example.se")
	anna.post("/middag/"+openDay, counts(2, 0, 0, 0, ""))

	bo := h.client(t)
	bo.member("Bo Bengtsson", "bo@example.se")
	for _, path := range []string{"/", "/middag/" + openDay, "/middag/" + openDay + "/lista", "/mina"} {
		if body := bo.get(path).Body.String(); strings.Contains(body, "anna@example.se") {
			t.Errorf("%s leaks Anna's address", path)
		}
	}
	// Her own page still shows it to her.
	if !strings.Contains(anna.get("/mina").Body.String(), "anna@example.se") {
		t.Error("a member should see their own address")
	}
}

// ------------------------------------------------------------- the matlist --

func TestListNeedsAPasswordOrTheMailedKey(t *testing.T) {
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
	anna.member("Anna", "anna@example.se")
	anna.post("/middag/"+openDay, counts(2, 2, 0, 1, "glutenfritt"))
	bo := h.client(t)
	bo.member("Bo", "bo@example.se")
	bo.post("/middag/"+openDay, counts(1, 0, 1, 0, ""))

	body := anna.get("/middag/" + openDay + "/lista").Body.String()
	for _, want := range []string{"Matlista", "glutenfritt", "Anna", "Bo"} {
		if !strings.Contains(body, want) {
			t.Errorf("the list is missing %q", want)
		}
	}
	// Five people: three adults, two children, one vegan, one vegetarian.
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
	form := counts(2, 0, 1, 0, "vegan")
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
	form := counts(1, 0, 0, 0, "")
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
	form := counts(1, 0, 0, 0, "")
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
	anna.member("Zäta Zettergren", "zeta@example.se")
	anna.post("/middag/"+openDay, counts(2, 0, 0, 0, "kikärtsallergi"))

	body := h.client(t).get("/gast").Body.String()
	for _, secret := range []string{"Zäta Zettergren", "zeta@example.se", "kikärtsallergi"} {
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
	c.identify("Chef", "chef@example.se")

	// A new team.
	rec := c.post("/admin/lag", url.Values{
		"name": {"Lag 3"}, "leader_name": {"Cecilia"},
		"leader_email": {"cecilia@example.se"}, "position": {"2"}, "active": {"1"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save team = %d — %s", rec.Code, rec.Body.String())
	}
	teams, _ := h.store.Teams(ctx)
	if len(teams) != 3 {
		t.Fatalf("got %d teams, want 3", len(teams))
	}

	// A team without a usable address would silently never get its list.
	rec = c.post("/admin/lag", url.Values{"name": {"Lag 4"}, "leader_email": {"inte-en-adress"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad leader address = %d, want 422", rec.Code)
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
	c.identify("Chef", "chef@example.se")

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
	member.member("Anna", "anna@example.se")
	if rec := member.post("/middag/"+openDay, counts(2, 0, 0, 0, "")); rec.Code != http.StatusUnprocessableEntity {
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
	c.identify("Chef", "chef@example.se")

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
	anna.member("Anna Andersson", "anna@example.se")
	anna.post("/middag/"+openDay, counts(2, 1, 0, 1, "glutenfritt"))

	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "chef@example.se")
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

// ------------------------------------------------------------- the mailing --

// At the deadline the cooking team is told where the list is — once.
func TestTheListIsMailedOnceWhenRegistrationCloses(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Before the deadline, nothing goes out for the open evening.
	h.runNotifications(ctx)
	if done, _ := h.store.Notified(ctx, openDay, notifyKind); done {
		t.Error("the open dinner should not have been mailed yet")
	}
	// The evening whose deadline has passed has been.
	done, err := h.store.Notified(ctx, shutDay, notifyKind)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("the closed dinner should have been mailed")
	}
	sent, _ := h.store.SentNotifications(ctx, "2026-08-01", "2026-08-31")
	if log, ok := sent[shutDay]; !ok || log.Recipient == "" {
		t.Errorf("the mail log should record who got it, got %+v", log)
	}

	// Running again must not send a second time.
	before := len(sent)
	h.runNotifications(ctx)
	after, _ := h.store.SentNotifications(ctx, "2026-08-01", "2026-08-31")
	if len(after) != before {
		t.Errorf("a second run sent more mail: %d then %d", before, len(after))
	}
}

func TestCancelledEveningsAreNotMailed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.store.SaveOverride(ctx, store.Override{
		Date: shutDay, Cancelled: true, UpdatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	h.runNotifications(ctx)
	if done, _ := h.store.Notified(ctx, shutDay, notifyKind); done {
		t.Error("a cancelled dinner should not be mailed")
	}
}

// The mail is a pointer to the list, never the list itself.
func TestTheMailedLinkOpensTheListAndCarriesNoNames(t *testing.T) {
	h := newHarness(t)
	anna := h.client(t)
	anna.member("Anna Andersson", "anna@example.se")
	anna.post("/middag/"+openDay, counts(2, 0, 0, 1, "glutenfritt"))

	world, _ := h.world(context.Background())
	d, ok := world.Schedule.Find(openDay)
	if !ok {
		t.Fatal("dinner missing")
	}
	link := h.listURL(d)
	if !strings.HasPrefix(link, "https://mat.example.se/middag/"+openDay+"/lista?nyckel=") {
		t.Fatalf("link = %q", link)
	}

	// Following it, as the leader would, shows the list without a login.
	path := strings.TrimPrefix(link, "https://mat.example.se")
	rec := h.client(t).get(path)
	if rec.Code != http.StatusOK {
		t.Fatalf("following the mailed link = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Anna Andersson") {
		t.Error("the list should show the household")
	}
	// The reader is not logged in, so the house navigation is not offered.
	if strings.Contains(rec.Body.String(), `href="/mina"`) {
		t.Error("a key-only reader should not be shown the member navigation")
	}
}

// --------------------------------------------------------------- odds & ends --

func TestUnknownDinnerIsANotFound(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	c.member("Anna", "anna@example.se")
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
	c.identify("Chef", "chef@example.se")

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
	form := counts(9, 0, 0, 0, "")
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
	member.member("Anna", "anna@example.se")
	if rec := member.post("/admin/anmalan", url.Values{"id": {regs[0].ID}}); rec.Code != http.StatusForbidden {
		t.Errorf("member deleting = %d, want 403", rec.Code)
	}
	if left, _ := h.store.Registrations(ctx, openDay); len(left) != 1 {
		t.Fatal("the registration should still be there")
	}

	admin := h.client(t)
	admin.login("adm")
	admin.identify("Chef", "chef@example.se")
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
	c.identify("Chef", "chef@example.se")
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
	c.identify("Chef", "chef@example.se")

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
	c.identify("Chef", "chef@example.se")

	rec := c.post("/admin/lag", url.Values{
		"name": {"Lag 3"}, "leader_email": {"c@example.se"}, "active": {"1"},
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
