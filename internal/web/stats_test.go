package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/store"
)

// servedEvenings fills the test season's first weeks — all of them before
// testNow — with one evening of each kind of party, and cancels the last.
func servedEvenings(t *testing.T, h *harness) {
	t.Helper()
	ctx := context.Background()
	at := testNow.AddDate(0, 0, -30)
	for _, r := range []store.Registration{
		{ID: "a", Date: "2026-08-04", Kind: store.KindMember, Member: "anna",
			Name: "Anna", Adults: 2, Children: 1},
		{ID: "b", Date: "2026-08-06", Kind: store.KindGuest, Name: "Besök",
			Adults: 1, Token: "tok"},
		{ID: "c", Date: "2026-08-11", Kind: store.KindMember, Member: "bo",
			Name: "Bo", Adults: 1, Guests: 2},
		{ID: "d", Date: "2026-08-13", Kind: store.KindLate, Name: "Glömsk",
			Adults: 2},
		// On the evening that is cancelled, so it must not count.
		{ID: "e", Date: "2026-08-18", Kind: store.KindMember, Member: "anna",
			Name: "Anna", Adults: 5},
		// Still to come, so it is the next dinner and not a meal served.
		{ID: "f", Date: "2026-08-20", Kind: store.KindMember, Member: "bo",
			Name: "Bo", Adults: 4},
	} {
		r.Diet = store.DietOmnivore
		r.CreatedAt, r.UpdatedAt = at, at
		if err := h.store.SaveRegistration(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.SaveOverride(ctx, store.Override{
		Date: "2026-08-18", Cancelled: true, UpdatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStatsCountOnlyServedDinners(t *testing.T) {
	h := newHarness(t)
	servedEvenings(t, h)
	ctx := context.Background()
	world, err := h.world(ctx)
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := h.summarize(ctx, world.Schedule.Dinners)
	if err != nil {
		t.Fatal(err)
	}
	p := buildStats(i18n.SV, world.Schedule, world.Seasons, summaries, "", testNow)

	want := tally{Meals: 3 + 1 + 3 + 2, Dinners: 4}
	for name, got := range map[string]tally{"month": p.Month, "season": p.Season, "all": p.All} {
		if got != want {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}
	if p.Next == nil || p.Next.Dinner.Key != "2026-08-20" || p.Next.People() != 4 {
		t.Errorf("next = %+v, want today's dinner with 4 booked", p.Next)
	}

	sc := p.Scope
	if sc.Name != "Test" {
		t.Errorf("scope = %q, want the current season", sc.Name)
	}
	if sc.Booked != 3+1+2 || sc.Brought != 2 || sc.External != 1 || sc.Standing != 0 {
		t.Errorf("parts: booked %d brought %d external %d standing %d",
			sc.Booked, sc.Brought, sc.External, sc.Standing)
	}
	if sc.Late != 1 || sc.Cancelled != 1 || sc.Households != 2 {
		t.Errorf("late %d cancelled %d households %d", sc.Late, sc.Cancelled, sc.Households)
	}
	if sc.Average != "2,3" {
		t.Errorf("average = %q, want 2,3", sc.Average)
	}
	if sc.Busiest == nil || sc.Busiest.Dinner.Key != "2026-08-04" {
		t.Errorf("busiest = %+v", sc.Busiest)
	}
	for _, e := range sc.Evenings {
		if e.Standing+e.Booked+e.Brought+e.External != e.People() {
			t.Errorf("%s: the parts do not add up to %d", e.Dinner.Key, e.People())
		}
	}
}

// A household on a standing registration is counted as standing, and its
// one-off guests with the guests it brought.
func TestStatsSplitTheStandingFromTheBooked(t *testing.T) {
	s := dinner.Summary{
		People: 7,
		Attendees: []dinner.Attendee{
			{Name: "A", Adults: 2, Standing: true, Member: "a"},
			{Name: "B", Adults: 1, Children: 1, Guests: 1, Member: "b"},
			{Name: "C", Adults: 1, Guest: true, Standing: true},
			{Name: "D", Adults: 1, Late: true},
		},
	}
	e := splitEvening(dinner.Dinner{}, s)
	if e.Standing != 2 || e.Booked != 3 || e.Brought != 1 || e.External != 1 {
		t.Errorf("split = %+v", e)
	}
}

func TestStatsAreForTheAdministratorOnly(t *testing.T) {
	h := newHarness(t)
	servedEvenings(t, h)

	member := h.client(t)
	member.member("Anna", "anna.andersson")
	if rec := member.get("/admin?flik=statistik"); rec.Code != http.StatusForbidden {
		t.Errorf("member at the statistics = %d, want 403", rec.Code)
	}

	c := h.client(t)
	c.login("adm")
	c.identify("Chef", "cecilia.dahl")
	rec := c.get("/admin?flik=statistik")
	if rec.Code != http.StatusOK {
		t.Fatalf("statistics = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Ätande per middag", "Hur de kom", "Portioner per månad",
		"på 4 middagar", `class="chart-band s4"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the statistics should show %q", want)
		}
	}
	if strings.Contains(body, "⟦") {
		t.Error("a phrase is missing from the catalogue")
	}

	// All time, and a season with nothing served, both render.
	if rec := c.get("/admin?flik=statistik&sasong=alla"); rec.Code != http.StatusOK {
		t.Errorf("all time = %d", rec.Code)
	}
}

func TestNiceMaxDividesIntoRoundWholeSteps(t *testing.T) {
	for _, c := range []struct {
		in        int
		top, step float64
	}{
		{0, 4, 1}, {3, 3, 1}, {13, 15, 5}, {42, 60, 20}, {97, 100, 25},
	} {
		top, step := niceMax(c.in)
		if top != c.top || step != c.step {
			t.Errorf("niceMax(%d) = %g, %g; want %g, %g", c.in, top, step, c.top, c.step)
		}
	}
}
