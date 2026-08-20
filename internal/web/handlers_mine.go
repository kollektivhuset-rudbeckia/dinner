package web

import (
	"net/http"
	"time"

	"github.com/O5ten/dinners/internal/auth"
	"github.com/O5ten/dinners/internal/config"
	"github.com/O5ten/dinners/internal/store"
)

// standingRow is one dinner weekday and this household's default for it.
type standingRow struct {
	Weekday time.Weekday
	Form    regForm
	Set     bool
}

func (s *Server) handleMine(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	v.GuestOpen = world.Settings.GuestOpen

	standing, err := s.store.StandingByEmail(ctx, v.Ident.Email)
	if err != nil {
		s.fail(w, r, "read standing registrations", err)
		return
	}
	byWeekday := map[time.Weekday]store.Standing{}
	for _, st := range standing {
		byWeekday[st.Weekday] = st
	}

	rows := make([]standingRow, 0, 2)
	for _, wd := range dinnerWeekdays(world, s.cfg) {
		st, ok := byWeekday[wd]
		rows = append(rows, standingRow{
			Weekday: wd,
			Set:     ok,
			Form: regForm{
				Adults: st.Adults, Children: st.Children,
				Vegans: st.Vegans, Vegetarians: st.Vegetarians, Note: st.Note,
			},
		})
	}

	upcoming := world.Schedule.Upcoming(v.Now, 0)
	summaries, err := s.summarize(ctx, upcoming)
	if err != nil {
		s.fail(w, r, "summarize dinners", err)
		return
	}
	mine := make([]row, 0, len(upcoming))
	for _, d := range upcoming {
		sum := summaries[d.Key]
		m := findMine(sum, v.Ident.Email)
		if m == nil {
			continue
		}
		mine = append(mine, row{Dinner: d, Summary: sum, Mine: m, Open: d.Open(v.Now)})
	}

	v.Title = "Mina anmälningar"
	v.Data = map[string]any{
		"Standing": rows,
		"Mine":     mine,
		"Saved":    r.URL.Query().Get("sparat") != "",
	}
	s.render(w, r, http.StatusOK, "mine.html", v)
}

// dinnerWeekdays is the union of the evenings every season uses, so the
// standing registrations cover whatever the house actually cooks. Before any
// season exists it falls back to what config.yaml says.
func dinnerWeekdays(world *world, cfg *config.Config) []time.Weekday {
	seen := map[time.Weekday]bool{}
	var out []time.Weekday
	for _, se := range world.Seasons {
		for _, wd := range se.Weekdays {
			if !seen[wd] {
				seen[wd] = true
				out = append(out, wd)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, cfg.Dinner.ParsedWeekdays()...)
	}
	// Monday first, the way a Swedish week runs.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && mondayIndex(out[j]) < mondayIndex(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func mondayIndex(wd time.Weekday) int { return (int(wd) + 6) % 7 }

func (s *Server) handleStanding(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Formuläret kunde inte läsas", "Försök igen.")
		return
	}
	wd, err := config.ParseWeekday(r.FormValue("weekday"))
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "Okänd veckodag", "Försök igen.")
		return
	}
	form := readForm(r)
	// "Ta bort" clears the household's default for this weekday. Saving a
	// standing registration for nobody means the same thing, so both roads
	// lead to the same place.
	if r.FormValue("action") == "clear" {
		form = regForm{}
	}
	if problem := validateCounts(form); problem != "" {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Anmälan gick inte att spara", problem)
		return
	}
	err = s.store.SaveStanding(ctx, store.Standing{
		ID:        auth.ID(),
		Email:     v.Ident.Email,
		Weekday:   wd,
		Name:      v.Ident.Name,
		Apartment: v.Ident.Apartment,
		Adults:    form.Adults, Children: form.Children,
		Vegans: form.Vegans, Vegetarians: form.Vegetarians,
		Note:      form.Note,
		UpdatedAt: s.now(),
	})
	if err != nil {
		s.fail(w, r, "save standing registration", err)
		return
	}
	s.log.Info("standing registration saved", "weekday", wd.String(), "people", form.Adults+form.Children)
	http.Redirect(w, r, "/mina?sparat=1", http.StatusSeeOther)
}
