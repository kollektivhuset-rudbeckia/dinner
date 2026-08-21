package dinner

import (
	"testing"
	"time"

	"github.com/O5ten/dinners/internal/store"
)

func member(email, name string, adults, children int, diet store.Diet, note string) store.Registration {
	return store.Registration{
		ID: "r-" + email, Date: "2026-08-25", Kind: store.KindMember,
		Email: email, Name: name, Adults: adults, Children: children,
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

func standing(email, name string, adults, children int, diet store.Diet) store.Standing {
	return store.Standing{
		ID: "s-" + email, Email: email, Weekday: time.Tuesday, Name: name,
		Adults: adults, Children: children, Diet: diet,
	}
}

func TestResolveAddsUpTheHeadcountAndDiets(t *testing.T) {
	sum := Resolve(
		[]store.Registration{
			member("anna@x.se", "Anna", 2, 2, store.DietOmnivore, "glutenfritt för ett barn"),
			member("bo@x.se", "Bo", 1, 0, store.DietVegan, ""),
			member("dan@x.se", "Dan", 2, 0, store.DietFlexitarian, ""),
			guest("Kalle", "Anna", 2, 0, store.DietPescetarian, "skaldjursallergi"),
		},
		[]store.Standing{standing("cecilia@x.se", "Cecilia", 2, 1, store.DietVegetarian)},
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
	sum := Resolve([]store.Registration{
		member("a@x.se", "A", 1, 0, "", ""),
		member("b@x.se", "B", 2, 0, store.Diet("makrobiotisk"), ""),
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
	sum := Resolve([]store.Registration{
		member("a@x.se", "A", 2, 0, store.DietVegan, ""),
		member("b@x.se", "B", 1, 1, store.DietVegan, ""),
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
	sum := Resolve(
		[]store.Registration{member("anna@x.se", "Anna", 1, 0, store.DietOmnivore, "")},
		[]store.Standing{standing("anna@x.se", "Anna", 2, 3, store.DietOmnivore)},
	)
	if sum.People != 1 {
		t.Fatalf("People = %d, want 1 — the evening's answer should win", sum.People)
	}
	if sum.Households != 1 {
		t.Errorf("Households = %d, want 1 — the household must not be counted twice", sum.Households)
	}
}

func TestRegisteringNobodyIsHowYouSkipOneEvening(t *testing.T) {
	sum := Resolve(
		[]store.Registration{member("anna@x.se", "Anna", 0, 0, store.DietOmnivore, "")},
		[]store.Standing{
			standing("anna@x.se", "Anna", 2, 2, store.DietOmnivore),
			standing("bo@x.se", "Bo", 1, 0, store.DietOmnivore),
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
	sum := Resolve(
		[]store.Registration{member("anna@x.se", "Anna", 1, 0, store.DietOmnivore, "")},
		[]store.Standing{standing("bo@x.se", "Bo", 1, 0, store.DietOmnivore)},
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
	sum := Resolve(
		[]store.Registration{
			guest("Adam", "Cecilia", 1, 0, store.DietOmnivore, ""),
			member("cecilia@x.se", "Cecilia", 1, 0, store.DietOmnivore, ""),
			member("bo@x.se", "bo", 1, 0, store.DietOmnivore, ""),
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
	sum := Resolve(nil, nil)
	if !sum.Empty() {
		t.Error("a summary with nobody in it should be Empty")
	}
	if sum.People != 0 || sum.Households != 0 {
		t.Errorf("unexpected counts: %+v", sum)
	}
}
