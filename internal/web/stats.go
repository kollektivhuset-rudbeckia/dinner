package web

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/dinner"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/store"
)

// statsTab is the tab the statistics live on. They are the administrator's
// alone: the guests who registered on the public page are in them, and so is
// how often each household eats, neither of which is the whole house's
// business.
const statsTab = "statistik"

// allSeasons is the scope that covers every dinner there has ever been.
const allSeasons = "alla"

// evening is one served dinner, with its people split by how they came to be
// on the list. The four parts add up to Summary.People.
type evening struct {
	Dinner  dinner.Dinner
	Summary dinner.Summary
	// Standing is the households' own people who came on their standing
	// registration without answering for the evening.
	Standing int
	// Booked is the households' own people who answered for the evening,
	// including the ones the cooking team wrote in after the deadline.
	Booked int
	// Brought is the guests a household brought along.
	Brought int
	// External is everybody from outside the house: a visitor who registered
	// on the public page, and a regular guest.
	External int
}

// People is how many ate.
func (e evening) People() int { return e.Summary.People }

func splitEvening(d dinner.Dinner, s dinner.Summary) evening {
	e := evening{Dinner: d, Summary: s}
	for _, a := range s.Attendees {
		switch {
		case a.Guest:
			e.External += a.People()
		case a.Standing:
			e.Standing += a.Adults + a.Children
			e.Brought += a.Guests
		default:
			e.Booked += a.Adults + a.Children
			e.Brought += a.Guests
		}
	}
	return e
}

// tally is a count of meals and the dinners they were served at.
type tally struct {
	Meals   int
	Dinners int
}

func (t *tally) add(e evening) {
	t.Meals += e.People()
	t.Dinners++
}

// hbar is one row of a horizontal bar list: a label, its number, and how long
// the bar is next to the longest one in the same list.
type hbar struct {
	Label string
	Value int
	// Detail is a second, quieter figure, such as an average.
	Detail string
	Pct    float64
}

// scale sets every bar's length against the longest.
func scale(bars []hbar) []hbar {
	top := 0
	for _, b := range bars {
		if b.Value > top {
			top = b.Value
		}
	}
	for i := range bars {
		if top > 0 {
			bars[i].Pct = float64(bars[i].Value) * 100 / float64(top)
		}
	}
	return bars
}

// scopeOption is one entry in the row of scopes above the details.
type scopeOption struct {
	Key     string
	Name    string
	Current bool
}

// statsPage is everything the statistics tab shows.
type statsPage struct {
	// The headline counters do not follow the chosen scope: this month, the
	// season we are in and all time are what an administrator asks about.
	Month      tally
	MonthName  string
	Season     tally
	SeasonName string
	All        tally
	// Next is the next dinner to be served, with what is booked for it so far.
	Next *evening

	Scopes []scopeOption
	Scope  scopeStats
}

// scopeStats are the details for one season, or for all of them.
type scopeStats struct {
	Name      string
	Evenings  []evening
	Meals     int
	Average   string
	Busiest   *evening
	Quietest  *evening
	Adults    int
	Children  int
	Brought   int
	External  int
	Standing  int
	Booked    int
	Late      int
	Cancelled int
	// Households is how many of the house's households ate at least once.
	Households int

	Trend    trendChart
	Stack    stackChart
	Months   barChart
	Diets    []hbar
	Weekdays []hbar
	Teams    []hbar
	Faithful []hbar
}

// buildStats works the statistics out from the dinners of the schedule and
// what each of them was resolved to. Only dinners that have been served and
// were not cancelled count as meals.
func buildStats(lang i18n.Lang, sched dinner.Schedule, seasons []store.Season,
	summaries map[string]dinner.Summary, scope string, now time.Time) statsPage {

	var p statsPage
	current := seasonAt(seasons, now)
	if current != nil {
		p.SeasonName = current.Name
	}
	p.MonthName = i18n.Month(lang, now)
	month := now.Format("2006-01")

	var served []evening
	for _, d := range sched.Dinners {
		if d.Cancelled {
			continue
		}
		if !d.Over(now) {
			if p.Next == nil {
				e := splitEvening(d, summaries[d.Key])
				p.Next = &e
			}
			continue
		}
		e := splitEvening(d, summaries[d.Key])
		served = append(served, e)
		p.All.add(e)
		if current != nil && d.Season.ID == current.ID {
			p.Season.add(e)
		}
		if d.Key[:7] == month {
			p.Month.add(e)
		}
	}

	// The scope: the season asked for, else the one we are in, else the last
	// one anything was served in.
	var chosen *store.Season
	if scope != allSeasons {
		if id, err := strconv.ParseInt(scope, 10, 64); err == nil {
			for i := range seasons {
				if seasons[i].ID == id {
					chosen = &seasons[i]
				}
			}
		}
		if chosen == nil && current != nil && p.Season.Dinners > 0 {
			chosen = current
		}
		if chosen == nil && len(served) > 0 {
			last := served[len(served)-1].Dinner.Season
			for i := range seasons {
				if seasons[i].ID == last.ID {
					chosen = &seasons[i]
				}
			}
		}
	}

	for _, se := range seasons {
		p.Scopes = append(p.Scopes, scopeOption{
			Key: strconv.FormatInt(se.ID, 10), Name: se.Name,
			Current: chosen != nil && chosen.ID == se.ID,
		})
	}
	p.Scopes = append(p.Scopes, scopeOption{
		Key: allSeasons, Name: i18n.T(lang, "stats.alltime"), Current: chosen == nil,
	})

	var in []evening
	for _, e := range served {
		if chosen == nil || e.Dinner.Season.ID == chosen.ID {
			in = append(in, e)
		}
	}
	name := i18n.T(lang, "stats.alltime")
	if chosen != nil {
		name = chosen.Name
	}
	p.Scope = scopeFor(lang, name, in, sched, chosen, now)
	// The months only tell a story across seasons, so they always span them.
	p.Scope.Months = monthChart(lang, served, now)
	return p
}

func seasonAt(seasons []store.Season, now time.Time) *store.Season {
	today := now.Format("2006-01-02")
	for i, se := range seasons {
		if se.Start <= today && today <= se.End {
			return &seasons[i]
		}
	}
	return nil
}

func scopeFor(lang i18n.Lang, name string, in []evening, sched dinner.Schedule,
	chosen *store.Season, now time.Time) scopeStats {

	sc := scopeStats{Name: name, Evenings: in}
	for _, d := range sched.Dinners {
		if d.Cancelled && d.Over(now) && (chosen == nil || d.Season.ID == chosen.ID) {
			sc.Cancelled++
		}
	}

	type weekday struct{ meals, dinners int }
	byWeekday := map[time.Weekday]*weekday{}
	type team struct {
		name           string
		meals, dinners int
	}
	var teams []*team
	teamAt := map[int64]*team{}
	diets := map[store.Diet]int{}
	type household struct {
		name     string
		evenings int
	}
	homes := map[string]*household{}

	for i := range in {
		e := &in[i]
		sc.Meals += e.People()
		sc.Adults += e.Summary.Adults
		sc.Children += e.Summary.Children
		sc.Brought += e.Brought
		sc.External += e.External
		sc.Standing += e.Standing
		sc.Booked += e.Booked
		if sc.Busiest == nil || e.People() > sc.Busiest.People() {
			sc.Busiest = e
		}
		if sc.Quietest == nil || e.People() < sc.Quietest.People() {
			sc.Quietest = e
		}
		w := byWeekday[e.Dinner.Weekday()]
		if w == nil {
			w = &weekday{}
			byWeekday[e.Dinner.Weekday()] = w
		}
		w.meals += e.People()
		w.dinners++
		if t := e.Dinner.Team; t != nil {
			tm := teamAt[t.ID]
			if tm == nil {
				tm = &team{name: t.Name}
				teamAt[t.ID] = tm
				teams = append(teams, tm)
			}
			tm.meals += e.People()
			tm.dinners++
		}
		for _, c := range e.Summary.Diets {
			diets[c.Diet] += c.People
		}
		for _, a := range e.Summary.Attendees {
			if a.Late {
				sc.Late++
			}
			if a.Guest || a.Member == "" {
				continue
			}
			h := homes[a.Member]
			if h == nil {
				h = &household{name: a.Name}
				homes[a.Member] = h
			}
			h.evenings++
		}
	}
	sc.Households = len(homes)
	if len(in) > 0 {
		sc.Average = decimal(lang, float64(sc.Meals)/float64(len(in)))
	}

	for _, d := range store.Diets {
		sc.Diets = append(sc.Diets, hbar{Label: DietLabel(lang, d), Value: diets[d]})
	}
	scale(sc.Diets)

	// Monday first, the way the house's week runs.
	for _, wd := range allWeekdays() {
		w := byWeekday[wd]
		if w == nil {
			continue
		}
		sc.Weekdays = append(sc.Weekdays, hbar{
			Label:  i18n.TitleCase(i18n.WeekdayPlural(lang, wd)),
			Value:  w.meals,
			Detail: i18n.T(lang, "stats.perdinner", decimal(lang, float64(w.meals)/float64(w.dinners)), i18n.Count(lang, "dinner", w.dinners)),
		})
	}
	scale(sc.Weekdays)

	sort.SliceStable(teams, func(i, j int) bool { return teams[i].meals > teams[j].meals })
	for _, t := range teams {
		sc.Teams = append(sc.Teams, hbar{
			Label:  t.name,
			Value:  t.meals,
			Detail: i18n.T(lang, "stats.perdinner", decimal(lang, float64(t.meals)/float64(t.dinners)), i18n.Count(lang, "dinner", t.dinners)),
		})
	}
	scale(sc.Teams)

	var faithful []*household
	for _, h := range homes {
		faithful = append(faithful, h)
	}
	sort.Slice(faithful, func(i, j int) bool {
		if faithful[i].evenings != faithful[j].evenings {
			return faithful[i].evenings > faithful[j].evenings
		}
		return strings.ToLower(faithful[i].name) < strings.ToLower(faithful[j].name)
	})
	if len(faithful) > 8 {
		faithful = faithful[:8]
	}
	for _, h := range faithful {
		sc.Faithful = append(sc.Faithful, hbar{Label: h.name, Value: h.evenings})
	}
	scale(sc.Faithful)

	sc.Trend = trend(lang, in)
	sc.Stack = stack(lang, in)
	return sc
}

// decimal writes a number with one decimal, with the decimal mark the reader
// expects.
func decimal(lang i18n.Lang, f float64) string {
	// Halves round up, the way a person would: 2,25 is 2,3.
	s := strconv.FormatFloat(math.Round(f*10)/10, 'f', 1, 64)
	if lang == i18n.SV {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}
