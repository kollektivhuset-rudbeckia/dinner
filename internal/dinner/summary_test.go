package dinner

import (
	"testing"
	"time"

	"github.com/O5ten/dinners/internal/store"
)

func member(username, name string, adults, children int, diet store.Diet, note string) store.Registration {
	return store.Registration{
		ID: "r-" + username, Date: "2026-08-25", Kind: store.KindMember,
		Member: username, Name: name, Adults: adults, Children: children,
		Diet: diet, Note: note,
	}
}

func guest(name, host string, adults, children int, diet store.Diet, note string) store.Registration {
	return store.Registration{
		ID: "g-" + name, Date: "2026-08-25", Kind: store.KindGuest,
		Name: name, Host: host, Adults: adults, Children: children,
		Diet: diet, Note: note,
	}
}

func standing(username, name string, adults, children int, diet store.Diet) store.Standing {
	return store.Standing{
		ID: "s-" + username, Member: username, Weekday: time.Tuesday, Name: name,
		Adults: adults, Children: children, Diet: diet,
		// Long in force, which is the uninteresting case for the arithmetic.
		UpdatedAt: testCloses.AddDate(0, -1, 0),
	}
}

// testCloses is the deadline of the evening these tests resolve: the Friday
// before the Tuesday the registrations above are for.
var testCloses = time.Date(2026, 8, 21, 23, 59, 0, 0, time.UTC)

// resolve is Resolve with that deadline filled in. Most of these tests are
// about the arithmetic rather than about who was in time, and say so by not
// mentioning it.
func resolve(regs []store.Registration, standing []store.Standing) Summary {
	return Resolve(regs, standing, testCloses)
}

func TestResolveAddsUpTheHeadcountAndDiets(t *testing.T) {
	sum := resolve(
		[]store.Registration{
			member("anna", "Anna", 2, 2, store.DietOmnivore, "glutenfritt för ett barn"),
			member("bo", "Bo", 1, 0, store.DietVegan, ""),
			member("dan", "Dan", 2, 0, store.DietFlexitarian, ""),
			guest("Kalle", "Anna", 2, 0, store.DietPescetarian, "skaldjursallergi"),
		},
		[]store.Standing{standing("cecilia", "Cecilia", 2, 1, store.DietVegetarian)},
	)

	if sum.People != 12 {
		t.Errorf("People = %d, want 12", sum.People)
	}
	if sum.Adults != 9 || sum.Children != 3 {
		t.Errorf("adults/children = %d/%d, want 9/3", sum.Adults, sum.Children)
	}
	if sum.Households != 5 {
		t.Errorf("Households = %d, want 5", sum.Households)
	}
	if sum.Guests != 2 {
		t.Errorf("Guests = %d, want 2", sum.Guests)
	}
	// The diet applies to everyone in the registration, so the portions are
	// the registration's headcount.
	want := map[store.Diet]int{
		store.DietOmnivore:    4,
		store.DietFlexitarian: 2,
		store.DietPescetarian: 2,
		store.DietVegetarian:  3,
		store.DietVegan:       1,
	}
	for diet, people := range want {
		if got := sum.Count(diet); got != people {
			t.Errorf("%s = %d portions, want %d", diet, got, people)
		}
	}
	// The portions must add up to the headcount, always.
	total := 0
	for _, c := range sum.Diets {
		total += c.People
	}
	if total != sum.People {
		t.Errorf("the diets add up to %d but %d people are coming", total, sum.People)
	}
	// Every diet is listed, in the offered order, even at zero.
	if len(sum.Diets) != len(store.Diets) {
		t.Fatalf("got %d diet rows, want %d", len(sum.Diets), len(store.Diets))
	}
	for i, c := range sum.Diets {
		if c.Diet != store.Diets[i] {
			t.Errorf("row %d = %s, want %s", i, c.Diet, store.Diets[i])
		}
	}
	if len(sum.Notes) != 2 {
		t.Errorf("Notes = %d, want 2", len(sum.Notes))
	}
}

// A diet that is missing or from an older build still has to be fed.
func TestAnUnknownDietIsCountedAsEatingEverything(t *testing.T) {
	sum := resolve([]store.Registration{
		member("a", "A", 1, 0, "", ""),
		member("b", "B", 2, 0, store.Diet("makrobiotisk"), ""),
	}, nil)

	if got := sum.Count(store.DietOmnivore); got != 3 {
		t.Errorf("omnivores = %d, want 3", got)
	}
	total := 0
	for _, c := range sum.Diets {
		total += c.People
	}
	if total != 3 {
		t.Errorf("portions = %d, want 3 — nobody may be dropped", total)
	}
}

// Households per diet is what the team counts when laying the table.
func TestDietsCountHouseholdsAsWellAsPeople(t *testing.T) {
	sum := resolve([]store.Registration{
		member("a", "A", 2, 0, store.DietVegan, ""),
		member("b", "B", 1, 1, store.DietVegan, ""),
	}, nil)
	for _, c := range sum.Diets {
		if c.Diet != store.DietVegan {
			continue
		}
		if c.People != 4 || c.Households != 2 {
			t.Errorf("vegan = %d people from %d households, want 4 from 2", c.People, c.Households)
		}
	}
}

// The whole point of a one-off registration is that it beats the household's
// standing one, in both directions.
func TestRegistrationForTheEveningWinsOverTheStandingOne(t *testing.T) {
	sum := resolve(
		[]store.Registration{member("anna", "Anna", 1, 0, store.DietOmnivore, "")},
		[]store.Standing{standing("anna", "Anna", 2, 3, store.DietOmnivore)},
	)
	if sum.People != 1 {
		t.Fatalf("People = %d, want 1 — the evening's answer should win", sum.People)
	}
	if sum.Households != 1 {
		t.Errorf("Households = %d, want 1 — the household must not be counted twice", sum.Households)
	}
}

func TestRegisteringNobodyIsHowYouSkipOneEvening(t *testing.T) {
	sum := resolve(
		[]store.Registration{member("anna", "Anna", 0, 0, store.DietOmnivore, "")},
		[]store.Standing{
			standing("anna", "Anna", 2, 2, store.DietOmnivore),
			standing("bo", "Bo", 1, 0, store.DietOmnivore),
		},
	)
	if sum.People != 1 {
		t.Errorf("People = %d, want 1 (only Bo)", sum.People)
	}
	if len(sum.Attendees) != 1 || sum.Attendees[0].Name != "Bo" {
		t.Errorf("attendees = %+v, want just Bo", sum.Attendees)
	}
	// Saying no is an answer, and the cooking team should see it was given.
	if len(sum.Declined) != 1 || sum.Declined[0].Name != "Anna" {
		t.Errorf("declined = %+v, want Anna", sum.Declined)
	}
	// A household that is not coming contributes no dietary note.
	if len(sum.Notes) != 0 {
		t.Errorf("notes = %+v, want none", sum.Notes)
	}
}

func TestStandingHouseholdsAreMarkedAsSuch(t *testing.T) {
	sum := resolve(
		[]store.Registration{member("anna", "Anna", 1, 0, store.DietOmnivore, "")},
		[]store.Standing{standing("bo", "Bo", 1, 0, store.DietOmnivore)},
	)
	byName := map[string]Attendee{}
	for _, a := range sum.Attendees {
		byName[a.Name] = a
	}
	if byName["Anna"].Standing {
		t.Error("Anna answered for this evening and should not be marked standing")
	}
	if !byName["Bo"].Standing {
		t.Error("Bo is coming on his standing registration and should be marked")
	}
	if byName["Anna"].ID == "" {
		t.Error("an answered registration should carry its id")
	}
	if byName["Bo"].ID != "" {
		t.Error("a standing attendee has no registration row yet")
	}
}

func TestAttendeesAreSortedHouseFirstThenByName(t *testing.T) {
	sum := resolve(
		[]store.Registration{
			guest("Adam", "Cecilia", 1, 0, store.DietOmnivore, ""),
			member("cecilia", "Cecilia", 1, 0, store.DietOmnivore, ""),
			member("bo", "bo", 1, 0, store.DietOmnivore, ""),
		},
		nil,
	)
	var order []string
	for _, a := range sum.Attendees {
		order = append(order, a.Name)
	}
	want := []string{"bo", "Cecilia", "Adam"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestEmptySummary(t *testing.T) {
	sum := resolve(nil, nil)
	if !sum.Empty() {
		t.Error("a summary with nobody in it should be Empty")
	}
	if sum.People != 0 || sum.Households != 0 {
		t.Errorf("unexpected counts: %+v", sum)
	}
}

// Registering for one evening is refused once its deadline has passed. A
// standing registration must not be a way round that: one saved afterwards
// used to appear on a list the cooking team had already been given.
func TestAStandingRegistrationCannotBeatTheDeadline(t *testing.T) {
	late := standing("anna", "Anna", 2, 0, store.DietOmnivore)
	late.UpdatedAt = testCloses.Add(time.Minute)

	sum := Resolve(nil, []store.Standing{late}, testCloses)
	if sum.People != 0 {
		t.Errorf("People = %d, want 0 — the deadline had passed when it was saved", sum.People)
	}
	if len(sum.Attendees) != 0 {
		t.Errorf("attendees = %+v, want none", sum.Attendees)
	}
}

// The boundary is the same one registration uses: the deadline itself is too
// late, because Open() is now.Before(Closes).
func TestAStandingRegistrationSavedExactlyAtTheDeadlineIsTooLate(t *testing.T) {
	onTheDot := standing("anna", "Anna", 2, 0, store.DietOmnivore)
	onTheDot.UpdatedAt = testCloses
	if sum := Resolve(nil, []store.Standing{onTheDot}, testCloses); sum.People != 0 {
		t.Errorf("People = %d, want 0", sum.People)
	}

	justInTime := standing("bo", "Bo", 2, 0, store.DietOmnivore)
	justInTime.UpdatedAt = testCloses.Add(-time.Second)
	if sum := Resolve(nil, []store.Standing{justInTime}, testCloses); sum.People != 2 {
		t.Errorf("People = %d, want 2 — a second inside the deadline is in time", sum.People)
	}
}

// -------------------------------------------------------- regular guests --

// regular is an approved regular guest's standing registration: a friend of the
// house who is counted in every Tuesday without having an account here.
func regular(token, name, host string, adults, children int, diet store.Diet) store.Standing {
	return store.Standing{
		ID: "sg-" + token, Kind: store.KindGuest, Token: token,
		Weekday: time.Tuesday, Name: name, Host: host,
		Adults: adults, Children: children, Diet: diet,
		Status:    store.StatusApproved,
		UpdatedAt: testCloses.AddDate(0, -1, 0),
	}
}

// exception is what a regular says about one evening instead.
func exception(token, name, host string, adults, children int, diet store.Diet) store.Registration {
	return store.Registration{
		ID: "eg-" + token, Date: "2026-08-25", Kind: store.KindGuest,
		Name: name, Host: host, Adults: adults, Children: children,
		Diet: diet, StandingToken: token,
	}
}

func TestAnApprovedRegularIsCountedAsAGuestOnAStandingRegistration(t *testing.T) {
	sum := resolve(nil, []store.Standing{
		regular("t1", "Kalle", "Anna", 2, 1, store.DietVegan),
	})
	if sum.People != 3 {
		t.Fatalf("People = %d, want 3", sum.People)
	}
	if sum.Guests != 3 {
		t.Errorf("Guests = %d, want the regular counted as a visitor", sum.Guests)
	}
	if len(sum.Attendees) != 1 {
		t.Fatalf("attendees = %+v", sum.Attendees)
	}
	got := sum.Attendees[0]
	if !got.Guest || !got.Standing {
		t.Errorf("the cooking team should see both that they are a guest and that "+
			"they did not register: %+v", got)
	}
	if got.Host != "Anna" {
		t.Errorf("Host = %q, want the household they eat with", got.Host)
	}
	if got.Member != "" {
		t.Errorf("Member = %q, want none — a regular has no account", got.Member)
	}
	if sum.Count(store.DietVegan) != 3 {
		t.Errorf("vegan portions = %d, want 3", sum.Count(store.DietVegan))
	}
}

// Nobody has agreed to it, so it is a request and not a rule. This is the whole
// difference between a regular and a household, and it fails closed: a status
// that says nothing is not an approval.
func TestARegularNobodyHasApprovedIsNotCounted(t *testing.T) {
	waiting := regular("t1", "Kalle", "Anna", 2, 0, store.DietOmnivore)
	waiting.Status = store.StatusPending
	if sum := resolve(nil, []store.Standing{waiting}); sum.People != 0 {
		t.Errorf("People = %d, want 0 while the request is waiting", sum.People)
	}

	unsaid := regular("t2", "Stina", "Bo", 2, 0, store.DietOmnivore)
	unsaid.Status = ""
	if sum := resolve(nil, []store.Standing{unsaid}); sum.People != 0 {
		t.Errorf("People = %d, want 0 — a row that says nothing was approved by nobody",
			sum.People)
	}

	// A household's own needs nobody's permission, and one built without a
	// status is still a household's own.
	if sum := resolve(nil, []store.Standing{standing("anna", "Anna", 2, 0, store.DietOmnivore)}); sum.People != 2 {
		t.Errorf("People = %d, want 2 — a household approves its own", sum.People)
	}
}

// A regular has no account, so what ties their answer for one evening to their
// standing registration is the link, and it has to beat it exactly as a
// household's own answer does.
func TestARegularsAnswerForTheEveningWinsOverTheirStandingOne(t *testing.T) {
	reg := regular("t1", "Kalle", "Anna", 2, 0, store.DietOmnivore)

	sum := resolve(
		[]store.Registration{exception("t1", "Kalle", "Anna", 4, 1, store.DietVegan)},
		[]store.Standing{reg},
	)
	if sum.People != 5 {
		t.Errorf("People = %d, want 5 — the evening's own answer", sum.People)
	}
	if sum.Households != 1 {
		t.Errorf("Households = %d, want 1 — not the standing one as well", sum.Households)
	}

	// And an answer for nobody is how they sit one evening out.
	sum = resolve(
		[]store.Registration{exception("t1", "Kalle", "Anna", 0, 0, store.DietOmnivore)},
		[]store.Standing{reg},
	)
	if sum.People != 0 {
		t.Errorf("People = %d, want 0", sum.People)
	}
	if len(sum.Declined) != 1 {
		t.Errorf("the cooking team should see the answer was given: %+v", sum.Declined)
	}
}

// Two regulars with no accounts between them are two different people, and a
// one-off visitor's registration is nobody's exception.
func TestRegularsAreToldApartByTheirOwnLinks(t *testing.T) {
	sum := resolve(
		[]store.Registration{
			// A visitor who just registered for this evening, and an exception
			// belonging to one of the two regulars.
			guest("Stina", "Bo", 1, 0, store.DietOmnivore, ""),
			exception("t1", "Kalle", "Anna", 1, 0, store.DietOmnivore),
		},
		[]store.Standing{
			regular("t1", "Kalle", "Anna", 2, 0, store.DietOmnivore),
			regular("t2", "Doris", "Cecilia", 3, 0, store.DietOmnivore),
		},
	)
	// Kalle's one, Stina's one, and Doris's three, still on her own standing
	// registration.
	if sum.People != 5 {
		t.Errorf("People = %d, want 5: %+v", sum.People, sum.Attendees)
	}
	if sum.Households != 3 {
		t.Errorf("Households = %d, want 3", sum.Households)
	}
}

// Approval is what dates a regular's standing registration, so a request agreed
// to after the deadline belongs to the evenings after it.
func TestARegularApprovedAfterTheDeadlineIsNotOnTheListAlreadySent(t *testing.T) {
	late := regular("t1", "Kalle", "Anna", 2, 0, store.DietOmnivore)
	late.UpdatedAt = testCloses.Add(time.Minute)
	if sum := resolve(nil, []store.Standing{late}); sum.People != 0 {
		t.Errorf("People = %d, want 0 — the list had gone out when it was approved",
			sum.People)
	}
}
