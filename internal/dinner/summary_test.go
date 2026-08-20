package dinner

import (
	"testing"
	"time"

	"github.com/O5ten/dinners/internal/store"
)

func member(email, name string, adults, children, vegans, vegetarians int, note string) store.Registration {
	return store.Registration{
		ID: "r-" + email, Date: "2026-08-25", Kind: store.KindMember,
		Email: email, Name: name, Adults: adults, Children: children,
		Vegans: vegans, Vegetarians: vegetarians, Note: note,
	}
}

func guest(name, host string, adults, children int, note string) store.Registration {
	return store.Registration{
		ID: "g-" + name, Date: "2026-08-25", Kind: store.KindGuest,
		Name: name, Host: host, Adults: adults, Children: children, Note: note,
	}
}

func standing(email, name string, adults, children, vegans, vegetarians int) store.Standing {
	return store.Standing{
		ID: "s-" + email, Email: email, Weekday: time.Tuesday, Name: name,
		Adults: adults, Children: children, Vegans: vegans, Vegetarians: vegetarians,
	}
}

func TestResolveAddsUpTheHeadcountAndDiets(t *testing.T) {
	sum := Resolve(
		[]store.Registration{
			member("anna@x.se", "Anna", 2, 2, 0, 1, "glutenfritt för ett barn"),
			member("bo@x.se", "Bo", 1, 0, 1, 0, ""),
			guest("Kalle", "Anna", 2, 0, "skaldjursallergi"),
		},
		[]store.Standing{standing("cecilia@x.se", "Cecilia", 2, 1, 2, 0)},
	)

	if sum.People != 10 {
		t.Errorf("People = %d, want 10", sum.People)
	}
	if sum.Adults != 7 || sum.Children != 3 {
		t.Errorf("adults/children = %d/%d, want 7/3", sum.Adults, sum.Children)
	}
	if sum.Vegans != 3 || sum.Vegetarians != 1 {
		t.Errorf("vegans/vegetarians = %d/%d, want 3/1", sum.Vegans, sum.Vegetarians)
	}
	// Everyone not counted as vegan or vegetarian eats what is served.
	if sum.Omnivores != 6 {
		t.Errorf("Omnivores = %d, want 6", sum.Omnivores)
	}
	if sum.Households != 4 {
		t.Errorf("Households = %d, want 4", sum.Households)
	}
	if sum.Guests != 2 {
		t.Errorf("Guests = %d, want 2", sum.Guests)
	}
	// Bo and the standing household wrote nothing, so two notes.
	if len(sum.Notes) != 2 {
		t.Errorf("Notes = %d, want 2", len(sum.Notes))
	}
}

// The whole point of a one-off registration is that it beats the household's
// standing one, in both directions.
func TestRegistrationForTheEveningWinsOverTheStandingOne(t *testing.T) {
	sum := Resolve(
		[]store.Registration{member("anna@x.se", "Anna", 1, 0, 0, 0, "")},
		[]store.Standing{standing("anna@x.se", "Anna", 2, 3, 0, 0)},
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
		[]store.Registration{member("anna@x.se", "Anna", 0, 0, 0, 0, "")},
		[]store.Standing{
			standing("anna@x.se", "Anna", 2, 2, 0, 0),
			standing("bo@x.se", "Bo", 1, 0, 0, 0),
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
		[]store.Registration{member("anna@x.se", "Anna", 1, 0, 0, 0, "")},
		[]store.Standing{standing("bo@x.se", "Bo", 1, 0, 0, 0)},
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
			guest("Adam", "Cecilia", 1, 0, ""),
			member("cecilia@x.se", "Cecilia", 1, 0, 0, 0, ""),
			member("bo@x.se", "bo", 1, 0, 0, 0, ""),
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

// More vegans than people is rejected in the form, but a summary must never
// report a negative number of omnivores whatever is in the database.
func TestOmnivoresNeverGoNegative(t *testing.T) {
	sum := Resolve([]store.Registration{member("a@x.se", "A", 1, 0, 5, 5, "")}, nil)
	if sum.Omnivores != 0 {
		t.Errorf("Omnivores = %d, want 0", sum.Omnivores)
	}
	a := Attendee{Adults: 1, Vegans: 5}
	if a.Omnivores() != 0 {
		t.Errorf("Attendee.Omnivores = %d, want 0", a.Omnivores())
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
