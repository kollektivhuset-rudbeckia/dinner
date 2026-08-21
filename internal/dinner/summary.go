package dinner

import (
	"sort"
	"strings"

	"github.com/O5ten/dinners/internal/store"
)

// Attendee is one household at one dinner, whether it registered for that
// evening or is simply following its standing registration.
type Attendee struct {
	// ID is the registration's identifier, empty for a household that has not
	// answered for this evening and is coming on its standing registration.
	ID        string
	Name      string
	Apartment string
	Adults    int
	Children  int
	Diet      store.Diet
	Note      string
	// Host is the member a guest is visiting.
	Host  string
	Guest bool
	// Standing marks a household that has not answered for this evening; the
	// numbers come from its default for this weekday.
	Standing bool
	// Email identifies the household. It is deliberately never rendered to
	// anyone but that household and the administrator.
	Email string
}

// People is how many will eat.
func (a Attendee) People() int { return a.Adults + a.Children }

// Diet is chosen once for the whole registration, so every one of these
// people is served the same meal.

// Summary is everything the cooking team needs to know about one evening.
type Summary struct {
	// Attendees are the households that are coming, by name.
	Attendees []Attendee
	// Declined are the households whose standing registration would have
	// brought them, but who have said no for this evening. They are shown so
	// the team can see the answer was given rather than forgotten.
	Declined []Attendee

	Households int
	Adults     int
	Children   int
	People     int

	// Diets counts the people, not the households, behind each meal. Every
	// diet is present even at zero, so the cooking team reads the same rows
	// every week and can see that nobody needs the vegan pot rather than
	// wondering whether it was left out.
	Diets []DietCount

	// Guests counts visitors, who are already included in the totals above.
	Guests int
	// Notes are the free-text dietary restrictions, one per household that
	// wrote one.
	Notes []Note
}

// Note is one household's dietary restriction, with the name to ask about it.
type Note struct {
	Name string
	Text string
}

// DietCount is how many people are served one of the meals.
type DietCount struct {
	Diet store.Diet
	// People is how many portions of this meal are needed.
	People int
	// Households is how many registrations asked for it, which is what the
	// team counts when it lays the table rather than when it cooks.
	Households int
}

// Count returns the number of people on one diet.
func (s Summary) Count(d store.Diet) int {
	for _, c := range s.Diets {
		if c.Diet == d {
			return c.People
		}
	}
	return 0
}

// Resolve merges the answers given for one evening with the standing
// registrations for its weekday, and adds everything up.
//
// A registration for the evening always wins over the household's standing
// registration — including a registration for nobody, which is how a
// household says it is skipping this one.
func Resolve(regs []store.Registration, standing []store.Standing) Summary {
	answered := make(map[string]bool, len(regs))
	for _, r := range regs {
		if r.Kind == store.KindMember && r.Email != "" {
			answered[r.Email] = true
		}
	}

	var s Summary
	people := map[store.Diet]int{}
	households := map[store.Diet]int{}
	add := func(a Attendee) {
		if a.People() <= 0 {
			s.Declined = append(s.Declined, a)
			return
		}
		s.Attendees = append(s.Attendees, a)
		s.Households++
		s.Adults += a.Adults
		s.Children += a.Children
		diet := a.Diet
		if !diet.Valid() {
			// A blank or unknown diet has to be fed something, and the meal
			// with no restrictions is the safe guess to show the team.
			diet = store.DietOmnivore
		}
		people[diet] += a.People()
		households[diet]++
		if a.Guest {
			s.Guests += a.People()
		}
		if t := strings.TrimSpace(a.Note); t != "" {
			s.Notes = append(s.Notes, Note{Name: a.Name, Text: t})
		}
	}

	for _, r := range regs {
		add(Attendee{
			ID: r.ID, Name: r.Name, Apartment: r.Apartment,
			Adults: r.Adults, Children: r.Children, Diet: r.Diet,
			Note: r.Note, Host: r.Host, Guest: r.Guest(), Email: r.Email,
		})
	}
	for _, st := range standing {
		if answered[st.Email] {
			continue
		}
		add(Attendee{
			Name: st.Name, Apartment: st.Apartment,
			Adults: st.Adults, Children: st.Children, Diet: st.Diet,
			Note: st.Note, Standing: true, Email: st.Email,
		})
	}

	s.People = s.Adults + s.Children
	for _, d := range store.Diets {
		s.Diets = append(s.Diets, DietCount{
			Diet: d, People: people[d], Households: households[d],
		})
	}

	byName := func(list []Attendee) {
		sort.SliceStable(list, func(i, j int) bool {
			// Guests after the house, then alphabetically. The cooking team
			// reads this as a list of doors to knock on.
			if list[i].Guest != list[j].Guest {
				return !list[i].Guest
			}
			return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
		})
	}
	byName(s.Attendees)
	byName(s.Declined)
	sort.SliceStable(s.Notes, func(i, j int) bool {
		return strings.ToLower(s.Notes[i].Name) < strings.ToLower(s.Notes[j].Name)
	})
	return s
}

// Empty reports whether nobody at all has signed up.
func (s Summary) Empty() bool { return s.People == 0 }
