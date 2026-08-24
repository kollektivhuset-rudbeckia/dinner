package web

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/O5ten/dinners/internal/store"
)

// A regular is a friend of the house who eats here every week without being one
// of its households: no account in the chat, no password, nothing to send a
// confirmation to. What stands in for all of that is an administrator saying
// yes, and their own link afterwards.

// request is what a friend of the house fills in to ask to be counted in.
func request(name, host string, adults, children int, wds ...time.Weekday) url.Values {
	form := party(adults, children, store.DietOmnivore, "")
	form.Set("name", name)
	form.Set("host", host)
	for _, wd := range wds {
		form.Add("weekdays", itoa(int(wd)))
	}
	return form
}

// ask makes the request and returns the link the regular keeps.
func (c *client) ask(form url.Values) string {
	c.t.Helper()
	rec := c.post("/stamgast", form)
	if rec.Code != http.StatusSeeOther {
		c.t.Fatalf("asking to be a regular = %d — %s", rec.Code, rec.Body.String())
	}
	link := strings.Split(rec.Header().Get("Location"), "?")[0]
	if !strings.HasPrefix(link, "/stamgast/") {
		c.t.Fatalf("expected a link back to the request, got %q", link)
	}
	return link
}

// adminClient is a browser logged in with the administrator's password.
func (h *harness) adminClient(t *testing.T) *client {
	t.Helper()
	c := h.client(t)
	c.login("adm")
	c.identify("Cecilia Dahl", "cecilia.dahl")
	return c
}

// answer is the administrator's reply to one request, named by one of its rows
// rather than by the link the guest keeps.
func (c *client) answer(h *harness, token, action string) *httptest.ResponseRecorder {
	c.t.Helper()
	rows, err := h.store.StandingByToken(context.Background(), token)
	if err != nil || len(rows) == 0 {
		c.t.Fatalf("no request behind %q: %v", token, err)
	}
	return c.post("/admin/stamgast", url.Values{
		"id": {rows[0].ID}, "action": {action}, "flik": {"staende"},
	})
}

// approve is the administrator agreeing to one request.
func (c *client) approve(h *harness, token string) {
	c.t.Helper()
	if rec := c.answer(h, token, "approve"); rec.Code != http.StatusSeeOther {
		c.t.Fatalf("approve = %d — %s", rec.Code, rec.Body.String())
	}
}

func tokenOf(t *testing.T, link string) string {
	t.Helper()
	parts := strings.Split(strings.Trim(link, "/"), "/")
	if len(parts) != 2 {
		t.Fatalf("cannot read a token out of %q", link)
	}
	return parts[1]
}

func TestARegularIsOnNoListUntilAnAdministratorAgrees(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	guest := h.client(t)

	if rec := guest.get("/stamgast"); rec.Code != http.StatusOK {
		t.Fatalf("the request page needs no password, got %d", rec.Code)
	}
	link := guest.ask(request("Kalle Svensson", "Anna Andersson", 2, 0, time.Tuesday, time.Thursday))
	token := tokenOf(t, link)

	// One row per evening asked for, none of them counting yet.
	rows, err := h.store.StandingByToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("stored %d rows, want one per evening asked for", len(rows))
	}
	for _, row := range rows {
		if !row.Pending() || row.Host != "Anna Andersson" {
			t.Errorf("stored %+v", row)
		}
	}

	// The guest's own page says what is happening, and offers no evenings to
	// change while there is nothing agreed to change.
	body := guest.get(link).Body.String()
	if !strings.Contains(body, "Kalle Svensson") {
		t.Error("the regular should see their own request")
	}
	if !strings.Contains(body, "väntar på svar") {
		t.Error("the page should say the request is waiting")
	}

	// Nobody is cooking for them yet.
	member := h.client(t)
	member.member("Anna Andersson", "anna.andersson")
	if list := member.get("/middag/" + openDay + "/lista").Body.String(); strings.Contains(list, "Kalle") {
		t.Error("a request nobody has agreed to must not reach the cooking team's list")
	}

	// The administrator is told there is something waiting, whichever tab they
	// came to.
	admin := h.adminClient(t)
	schedule := admin.get("/admin?flik=schema").Body.String()
	if !strings.Contains(schedule, "tab-badge") {
		t.Error("the tab bar should show that a request is waiting")
	}
	queue := admin.get("/admin?flik=staende").Body.String()
	for _, want := range []string{"Kalle Svensson", "Anna Andersson", `value="approve"`} {
		if !strings.Contains(queue, want) {
			t.Errorf("the queue is missing %q", want)
		}
	}
	// The link is the guest's alone: it is what lets them change and withdraw
	// the arrangement, and it has no business on anybody else's screen.
	if strings.Contains(queue, token) {
		t.Error("the admin view is handing out the regular's own link")
	}

	admin.approve(h, token)

	// From now on they are counted in, without registering for anything.
	list := member.get("/middag/" + openDay + "/lista").Body.String()
	if !strings.Contains(list, "Kalle Svensson") {
		t.Error("an approved regular should be on the cooking team's list")
	}
	if !strings.Contains(list, "Anna Andersson") {
		t.Error("the household they eat with should be on the list too")
	}
	if regs, _ := h.store.Registrations(ctx, openDay); len(regs) != 0 {
		t.Errorf("a standing registration should not write a row per evening: %+v", regs)
	}
	// And the badge is gone.
	if strings.Contains(admin.get("/admin?flik=schema").Body.String(), "tab-badge") {
		t.Error("nothing is waiting any more")
	}
}

func TestARegularSitsOutOneEveningAndComesBack(t *testing.T) {
	h := newHarness(t)
	guest := h.client(t)
	link := guest.ask(request("Kalle Svensson", "Anna Andersson", 2, 0, time.Tuesday))
	h.adminClient(t).approve(h, tokenOf(t, link))

	member := h.client(t)
	member.member("Anna Andersson", "anna.andersson")

	rec := guest.post(link+"/kvall", url.Values{"date": {openDay}, "action": {"decline"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sitting out one evening = %d — %s", rec.Code, rec.Body.String())
	}
	list := member.get("/middag/" + openDay + "/lista").Body.String()
	if !strings.Contains(list, "Har tackat nej") {
		t.Error("the cooking team should see that the answer was given")
	}
	// The next Tuesday is untouched by one evening's answer.
	later := member.get("/middag/" + laterDay + "/lista").Body.String()
	if !strings.Contains(later, "Kalle Svensson") {
		t.Error("the standing registration should still hold for the other evenings")
	}

	// A different number of them, just this once.
	rec = guest.post(link+"/kvall", url.Values{
		"date": {openDay}, "adults": {"4"}, "children": {"1"},
		"diet": {string(store.DietVegan)},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("changing one evening = %d — %s", rec.Code, rec.Body.String())
	}
	list = member.get("/middag/" + openDay + "/lista").Body.String()
	if !strings.Contains(list, "Kalle Svensson") {
		t.Error("they are coming after all")
	}
	if !strings.Contains(list, "Vegan") {
		t.Error("the evening's own meal should be what they said")
	}

	// And back to as usual.
	rec = guest.post(link+"/kvall", url.Values{"date": {openDay}, "action": {"clear"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("back to as usual = %d", rec.Code)
	}
	if regs, _ := h.store.Registrations(context.Background(), openDay); len(regs) != 0 {
		t.Errorf("clearing should leave the standing registration alone: %+v", regs)
	}
}

// The evenings the cooking teams have already been given are not ours to
// rewrite, whether the change comes from the guest or from an administrator.
func TestRevokingARegularLeavesTheEveningsAlreadySentAlone(t *testing.T) {
	h := newHarness(t)
	guest := h.client(t)
	link := guest.ask(request("Kalle Svensson", "Anna Andersson", 2, 0, time.Tuesday))
	admin := h.adminClient(t)
	admin.approve(h, tokenOf(t, link))

	// A weekend passes: the Tuesday's deadline is behind us and its team is
	// shopping, while the Tuesday after it is still open.
	h.now = func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) }

	if rec := admin.answer(h, tokenOf(t, link), "remove"); rec.Code != http.StatusSeeOther {
		t.Fatalf("remove = %d — %s", rec.Code, rec.Body.String())
	}
	if rows, _ := h.store.StandingByToken(context.Background(), tokenOf(t, link)); len(rows) != 0 {
		t.Errorf("the standing registration should be gone, got %+v", rows)
	}

	member := h.client(t)
	member.member("Anna Andersson", "anna.andersson")
	if list := member.get("/middag/" + openDay + "/lista").Body.String(); !strings.Contains(list, "Kalle Svensson") {
		t.Error("the evening already sent should keep the numbers it was sent with")
	}
	if list := member.get("/middag/" + laterDay + "/lista").Body.String(); strings.Contains(list, "Kalle Svensson") {
		t.Error("the evenings still open should have lost them")
	}
}

// Approval is not a way round the deadline either: a request agreed to after a
// list went out belongs to the evenings after it.
func TestApprovingAfterTheDeadlineDoesNotReachTheListAlreadySent(t *testing.T) {
	h := newHarness(t)
	guest := h.client(t)
	link := guest.ask(request("Kalle Svensson", "Anna Andersson", 2, 0, time.Tuesday))

	h.now = func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) }
	h.adminClient(t).approve(h, tokenOf(t, link))

	member := h.client(t)
	member.member("Anna Andersson", "anna.andersson")
	if list := member.get("/middag/" + openDay + "/lista").Body.String(); strings.Contains(list, "Kalle") {
		t.Error("the list the cooking team already has must not gain a person")
	}
	if list := member.get("/middag/" + laterDay + "/lista").Body.String(); !strings.Contains(list, "Kalle") {
		t.Error("the evenings still open should count them in")
	}
}

func TestARegularChangesTheNumbersAndWithdraws(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	guest := h.client(t)
	link := guest.ask(request("Kalle Svensson", "Anna Andersson", 2, 0, time.Tuesday))
	token := tokenOf(t, link)
	h.adminClient(t).approve(h, token)

	change := request("Kalle Svensson", "Anna Andersson", 1, 2)
	change.Set("diet", string(store.DietVegetarian))
	rec := guest.post(link, change)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("changing = %d — %s", rec.Code, rec.Body.String())
	}
	rows, _ := h.store.StandingByToken(ctx, token)
	if len(rows) != 1 || rows[0].Adults != 1 || rows[0].Children != 2 {
		t.Fatalf("stored %+v", rows)
	}
	// A change is not a new request: they stay approved.
	if !rows[0].Counts() {
		t.Error("changing the numbers should not send them back to the queue")
	}

	// Nobody at all is refused rather than quietly deleting the arrangement.
	empty := request("Kalle Svensson", "Anna Andersson", 0, 0)
	if rec := guest.post(link, empty); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("nobody = %d, want 422", rec.Code)
	}

	rec = guest.post(link, url.Values{"action": {"delete"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("withdrawing = %d", rec.Code)
	}
	if rows, _ := h.store.StandingByToken(ctx, token); len(rows) != 0 {
		t.Errorf("the standing registration should be gone, got %+v", rows)
	}
	if rec := guest.get(link); rec.Code != http.StatusNotFound {
		t.Errorf("the link should be dead afterwards, got %d", rec.Code)
	}
}

func TestARequestNeedsAnEveningAHostAndSomebodyToEat(t *testing.T) {
	h := newHarness(t)
	c := h.client(t)
	for _, bad := range []struct {
		why  string
		form url.Values
	}{
		{"no name", request("", "Anna Andersson", 2, 0, time.Tuesday)},
		{"no evening", request("Kalle", "Anna Andersson", 2, 0)},
		{"nobody", request("Kalle", "Anna Andersson", 0, 0, time.Tuesday)},
		// An evening the house does not cook on is not one to ask for.
		{"a Monday", request("Kalle", "Anna Andersson", 2, 0, time.Monday)},
	} {
		if rec := c.post("/stamgast", bad.form); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422", bad.why, rec.Code)
		}
	}
	if rows, _ := h.store.PendingStanding(context.Background()); len(rows) != 0 {
		t.Errorf("nothing should have been stored, got %+v", rows)
	}
}

// Plenty of regulars are friends or family of the house rather than one
// household's visitor, so naming somebody in the house is a help and not a gate.
func TestARegularNeedNotBeAnybodysVisitor(t *testing.T) {
	h := newHarness(t)
	guest := h.client(t)
	link := guest.ask(request("Kalle Svensson", "", 2, 0, time.Tuesday))
	token := tokenOf(t, link)

	rows, _ := h.store.StandingByToken(context.Background(), token)
	if len(rows) != 1 || rows[0].Host != "" {
		t.Fatalf("stored %+v", rows)
	}
	// The administrator still sees the request, said to be what it is.
	admin := h.adminClient(t)
	if queue := admin.get("/admin?flik=staende").Body.String(); !strings.Contains(queue, "vän till huset") {
		t.Error("the queue should say a regular with no household named is a friend of the house")
	}
	admin.approve(h, token)

	member := h.client(t)
	member.member("Anna Andersson", "anna.andersson")
	if list := member.get("/middag/" + openDay + "/lista").Body.String(); !strings.Contains(list, "Kalle Svensson") {
		t.Error("they should be counted in like any other regular")
	}
}

// Only an administrator answers a request: a household with the house password
// is not the cooking teams.
func TestOnlyAnAdministratorAnswersARequest(t *testing.T) {
	h := newHarness(t)
	guest := h.client(t)
	link := guest.ask(request("Kalle", "Anna Andersson", 2, 0, time.Tuesday))
	token := tokenOf(t, link)

	rows, _ := h.store.StandingByToken(context.Background(), token)
	if len(rows) != 1 {
		t.Fatalf("stored %+v", rows)
	}
	id := url.Values{"id": {rows[0].ID}, "action": {"approve"}}

	member := h.client(t)
	member.member("Anna Andersson", "anna.andersson")
	if rec := member.post("/admin/stamgast", id); rec.Code != http.StatusForbidden {
		t.Errorf("a member approving = %d, want 403", rec.Code)
	}
	// A regular cannot approve themselves either: with no password at all they
	// are sent to the login page rather than into the administration.
	rec := guest.post("/admin/stamgast", id)
	if !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Errorf("the guest reached %q from the administration", rec.Header().Get("Location"))
	}
	rows, _ = h.store.StandingByToken(context.Background(), token)
	if len(rows) != 1 || !rows[0].Pending() {
		t.Errorf("the request should still be waiting, got %+v", rows)
	}

	// And a household's own standing registration is not the administration's
	// to approve or revoke: it belongs to the household.
	admin := h.adminClient(t)
	member.post("/stadigvarande", withWeekday(party(2, 0, store.DietOmnivore, ""), time.Tuesday))
	own, _ := h.store.StandingByMember(context.Background(), "anna.andersson")
	if len(own) != 1 {
		t.Fatalf("the household's own standing registration = %+v", own)
	}
	rec = admin.post("/admin/stamgast", url.Values{"id": {own[0].ID}, "action": {"remove"}})
	if rec.Code != http.StatusNotFound {
		t.Errorf("revoking a household's own = %d, want 404", rec.Code)
	}
	if list, _ := h.store.StandingByMember(context.Background(), "anna.andersson"); len(list) != 1 {
		t.Error("the household's own standing registration should be untouched")
	}
}

// Turning guests away turns the request page off with them, but a regular who
// already has a link can still read it and withdraw.
func TestTheRequestPageFollowsTheGuestSetting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	guest := h.client(t)
	link := guest.ask(request("Kalle", "Anna Andersson", 2, 0, time.Tuesday))

	if err := h.store.SaveSettings(ctx, store.Settings{
		DeadlineWeekday: time.Friday, DeadlineMinutes: 23*60 + 59,
		DeadlineWeeksBefore: 1, GuestOpen: false,
	}); err != nil {
		t.Fatal(err)
	}
	if rec := guest.get("/stamgast"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /stamgast = %d, want 404 when guests are turned off", rec.Code)
	}
	if rec := guest.post("/stamgast", request("Stina", "Anna", 1, 0, time.Tuesday)); rec.Code != http.StatusNotFound {
		t.Errorf("POST /stamgast = %d, want 404", rec.Code)
	}
	if rec := guest.get(link); rec.Code != http.StatusOK {
		t.Errorf("an existing regular's own page = %d, want it still readable", rec.Code)
	}
	if rec := guest.post(link, url.Values{"action": {"delete"}}); rec.Code != http.StatusOK {
		t.Errorf("withdrawing = %d, want it still possible", rec.Code)
	}
}

// The way in for somebody with no password is a button, not a sentence with a
// link in it.
func TestTheLoginPageOffersTheGuestWaysInAsButtons(t *testing.T) {
	h := newHarness(t)
	body := h.client(t).get("/login").Body.String()
	if !strings.Contains(body, `<a class="button block" href="/gast"`) {
		t.Error("the guest way in should be a button of its own")
	}
	if !strings.Contains(body, `href="/stamgast"`) {
		t.Error("the login page should offer the standing arrangement too")
	}

	if err := h.store.SaveSettings(context.Background(), store.Settings{
		DeadlineWeekday: time.Friday, DeadlineMinutes: 23*60 + 59,
		DeadlineWeeksBefore: 1, GuestOpen: false,
	}); err != nil {
		t.Fatal(err)
	}
	body = h.client(t).get("/login").Body.String()
	for _, gone := range []string{`href="/gast"`, `href="/stamgast"`} {
		if strings.Contains(body, gone) {
			t.Errorf("the login page still points at %s while guests are turned off", gone)
		}
	}
}

// A regular is a guest on the list and in the export, and one the cooking team
// can see is there every week rather than having registered.
func TestARegularIsMarkedAsAGuestAndAsStanding(t *testing.T) {
	h := newHarness(t)
	guest := h.client(t)
	link := guest.ask(request("Kalle Svensson", "Anna Andersson", 2, 1, time.Tuesday))
	h.adminClient(t).approve(h, tokenOf(t, link))

	admin := h.adminClient(t)
	body := admin.get("/middag/" + openDay + "/lista").Body.String()
	if !strings.Contains(body, "Anna Andersson") {
		t.Error("the list should say who in the house they eat with")
	}

	rec := admin.get("/admin/export.csv?fran=" + openDay + "&till=" + openDay)
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, row := range rows[1:] {
		if row[4] != "Kalle Svensson" {
			continue
		}
		found = true
		if row[7] != "ja" {
			t.Error("a regular is a guest in the export")
		}
		if row[9] != "ja" {
			t.Error("a regular is there on a standing registration")
		}
		if row[8] != "Anna Andersson" {
			t.Errorf("the export should name the household, got %q", row[8])
		}
	}
	if !found {
		t.Errorf("the regular is missing from the export: %v", rows)
	}
}
