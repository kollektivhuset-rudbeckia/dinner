package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/O5ten/dinners/internal/auth"
	"github.com/O5ten/dinners/internal/config"
	"github.com/O5ten/dinners/internal/i18n"
	"github.com/O5ten/dinners/internal/store"
)

// standingRow is one dinner weekday and this household's default for it.
type standingRow struct {
	Weekday time.Weekday
	Form    regForm
	Set     bool
	// From is the first evening on this weekday a change would actually reach:
	// the next one whose deadline has not passed. The evenings before it have
	// been sent to their cooking teams and are not ours to rewrite, so saying
	// which one this takes effect on is the difference between a rule and a
	// page that quietly ignored you.
	From time.Time
}

func (s *Server) handleMine(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	world, err := s.world(ctx)
	if err != nil {
		s.fail(w, r, "load schedule", err)
		return
	}
	v.GuestOpen = world.Settings.GuestOpen

	standing, err := s.store.StandingByMember(ctx, v.Ident.MMUsername)
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
		if !ok {
			st.Diet = store.DietOmnivore
		}
		rows = append(rows, standingRow{
			Weekday: wd,
			Set:     ok,
			Form: regForm{
				Adults: st.Adults, Children: st.Children,
				Diet: st.Diet, Note: st.Note,
			},
			From: nextOpen(world, wd, v.Now),
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
		m := findMine(sum, v.Ident.MMUsername)
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

// nextOpen is the first evening on one weekday that is still taking answers.
// The zero time means there is no such evening in the schedule, which is what
// a season that has run out looks like.
func nextOpen(world *world, wd time.Weekday, now time.Time) time.Time {
	for _, d := range world.Schedule.Dinners {
		if d.Weekday() == wd && d.Open(now) {
			return d.Date
		}
	}
	return time.Time{}
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
		s.errorPage(w, r, http.StatusBadRequest,
			"error.form", "error.form.detail")
		return
	}
	wd, err := config.ParseWeekday(r.FormValue("weekday"))
	if err != nil {
		s.errorPage(w, r, http.StatusBadRequest,
			"error.weekday", "error.form.detail")
		return
	}
	form := readForm(r)
	// "Ta bort" clears the household's default for this weekday. Saving a
	// standing registration for nobody means the same thing, so both roads
	// lead to the same place.
	if r.FormValue("action") == "clear" {
		form = regForm{Diet: store.DietOmnivore}
	}
	if problem := validateParty(v.Lang, form); problem != "" {
		s.renderError(w, r, http.StatusUnprocessableEntity,
			i18n.T(v.Lang, "error.standing"), problem)
		return
	}
	// The evenings whose deadline has already passed keep what they were
	// counted with, before the new values take over. Without this, the rule in
	// dinner.Resolve would cut the old standing registration off those
	// evenings too, and a household that has eaten every Thursday for a year
	// would vanish from tonight's list by editing next month's.
	if err := s.keepClosedEvenings(ctx, r, v, wd); err != nil {
		s.fail(w, r, "keep the closed evenings as they were sent", err)
		return
	}

	err = s.store.SaveStanding(ctx, store.Standing{
		ID:        auth.ID(),
		Member:    v.Ident.MMUsername,
		MMUserID:  v.Ident.MMUserID,
		Weekday:   wd,
		Name:      v.Ident.Name,
		Apartment: v.Ident.Apartment,
		Adults:    form.Adults,
		Children:  form.Children,
		Diet:      form.Diet,
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

// keepClosedEvenings writes the household's standing registration down as an
// answer for each evening on this weekday whose deadline has passed but which
// is still to come — the evenings the cooking team is already shopping for.
//
// A standing registration is read live, at the moment a list is drawn up, so
// on its own it is not a record of anything: changing it changes what those
// evenings say, and clearing it takes the household off a list it is already
// counted on. An answer for the date always wins over a standing
// registration, so writing one down freezes those evenings exactly as they
// were sent, and leaves the new values to the evenings still open.
func (s *Server) keepClosedEvenings(ctx context.Context, r *http.Request, v *view, wd time.Weekday) error {
	existing, err := s.store.StandingByMember(ctx, v.Ident.MMUsername)
	if err != nil {
		return err
	}
	var was *store.Standing
	for i := range existing {
		if existing[i].Weekday == wd {
			was = &existing[i]
		}
	}
	if was == nil {
		// Nothing was in force on this weekday, so no closed evening is
		// counting on anything. This is the case the deadline was being got
		// round: a brand new standing registration, which dinner.Resolve now
		// keeps off the evenings it was too late for.
		return nil
	}

	world, err := s.world(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for _, d := range world.Schedule.Dinners {
		if d.Weekday() != wd || !d.Closed(now) {
			continue
		}
		// The old standing registration has to have been in force for this
		// evening in the first place; if it was saved after this deadline too,
		// it was never counted and there is nothing to keep.
		if !was.UpdatedAt.Before(d.Closes) {
			continue
		}
		// An answer already given for the date is the household's own word on
		// the evening, and outranks anything derived from a standing one.
		if _, err := s.store.MemberRegistration(ctx, d.Key, was.Member); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		err = s.store.SaveRegistration(ctx, store.Registration{
			ID:        auth.ID(),
			Date:      d.Key,
			Kind:      store.KindMember,
			Member:    was.Member,
			MMUserID:  was.MMUserID,
			Name:      was.Name,
			Apartment: was.Apartment,
			Adults:    was.Adults,
			Children:  was.Children,
			Diet:      was.Diet,
			Note:      was.Note,
			Token:     auth.Token(),
			CreatedAt: now, UpdatedAt: now,
			CreatedIP: s.clientIP(r),
		})
		if err != nil {
			return err
		}
		s.log.Info("kept a closed evening as the cooking team was given it",
			"date", d.Key, "member", was.Member,
			"people", was.Adults+was.Children)
	}
	return nil
}
