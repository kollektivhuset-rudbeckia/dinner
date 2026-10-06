package web

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/O5ten/dinners/internal/i18n"
)

// The charts on the statistics tab are drawn here, as SVG, on the server. The
// site has no build step and its content policy allows no script from anywhere
// but itself, so there is no charting library to reach for; and a chart is only
// a few dozen numbers turned into coordinates. The templates get the
// coordinates ready-made and only have to write them out.
//
// Every chart is drawn into a fixed coordinate box and scaled to the width of
// the page by its viewBox. The hover layer is SVG's own: each dinner or month
// has an invisible column the width of its slot with a <title>, which the
// browser shows as a tooltip, and CSS lights up its guide line.

// frame is a chart's coordinate box and the plot area inside it.
type frame struct {
	W, H                     float64
	Left, Right, Top, Bottom float64
}

func newFrame() frame {
	return frame{W: 720, H: 260, Left: 36, Right: 708, Top: 14, Bottom: 232}
}

// ViewBox is the frame as an SVG viewBox.
func (f frame) ViewBox() string { return fmt.Sprintf("0 0 %g %g", f.W, f.H) }

// tick is one labelled position on an axis.
type tick struct {
	Pos   float64
	Label string
}

// column is the hover target for one dinner or month: the whole height of the
// plot over its slot, a guide line down its middle, and what the tooltip says.
type column struct {
	X, W  float64
	Guide float64
	Title string
	// DotY is where the line passes through this column, for the marker.
	DotY float64
}

// trendChart is the number of people at every dinner, as a line.
type trendChart struct {
	frame
	YTicks []tick
	XTicks []tick
	Line   string
	Cols   []column
	// Dots says whether every dinner gets a marker. With a few hundred dinners
	// they would merge into a second, thicker line, so then only the one under
	// the pointer is shown.
	Dots     bool
	AvgY     float64
	AvgLabel string
	Empty    bool
}

// layer is one band of the stacked chart.
type layer struct {
	Class string
	Name  string
	Path  string
	// LabelY is where the band's name sits at the right end of the chart, or
	// zero when the band is too thin there to hold it.
	LabelY float64
}

// stackChart is the people at every dinner split by how they came.
type stackChart struct {
	frame
	YTicks []tick
	XTicks []tick
	Layers []layer
	Cols   []column
	Empty  bool
}

// bar is one bar of a vertical bar chart.
type bar struct {
	X, W  float64
	Path  string
	Title string
}

// barChart is a value per month.
type barChart struct {
	frame
	YTicks []tick
	XTicks []tick
	Bars   []bar
	Cols   []column
	Empty  bool
}

// niceMax rounds a maximum up to a number the axis can be divided into about
// four round, whole steps of: 1, 2, 2.5 or 5 times a power of ten.
func niceMax(v int) (top, step float64) {
	if v <= 0 {
		return 4, 1
	}
	raw := float64(v) / 4
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		step = m * mag
		// People come whole, so a step of 2.5 people is no step at all.
		if step >= raw && step == math.Trunc(step) {
			break
		}
	}
	if step < 1 {
		step = 1
	}
	return step * math.Ceil(float64(v)/step), step
}

func yTicks(f frame, top, step float64) []tick {
	var out []tick
	for v := 0.0; v <= top+step/2; v += step {
		out = append(out, tick{Pos: yFor(f, v, top), Label: strconv.FormatFloat(v, 'f', -1, 64)})
	}
	return out
}

func yFor(f frame, v, top float64) float64 {
	return f.Bottom - (f.Bottom-f.Top)*v/top
}

// slots spreads n evenly spaced positions across the plot, each in the
// middle of its own slot, and returns them with the slot width.
func slots(f frame, n int) ([]float64, float64) {
	w := (f.Right - f.Left) / float64(n)
	xs := make([]float64, n)
	for i := range xs {
		xs[i] = f.Left + w*(float64(i)+.5)
	}
	return xs, w
}

// xTicks labels at most about seven of the positions, evenly picked, always
// including the first and the last.
func xTicks(xs []float64, label func(i int) string) []tick {
	n := len(xs)
	if n == 0 {
		return nil
	}
	every := int(math.Ceil(float64(n) / 7))
	var picked []int
	for i := 0; i < n; i += every {
		picked = append(picked, i)
	}
	// The last position is always labelled; if the one before it is too
	// close, it gives way rather than letting the two collide.
	if last := picked[len(picked)-1]; last != n-1 {
		if len(picked) > 1 && n-1-last < (every+1)/2 {
			picked = picked[:len(picked)-1]
		}
		picked = append(picked, n-1)
	}
	out := make([]tick, len(picked))
	for j, i := range picked {
		out[j] = tick{Pos: xs[i], Label: label(i)}
	}
	return out
}

func pt(x, y float64) string {
	return strconv.FormatFloat(x, 'f', 1, 64) + "," + strconv.FormatFloat(y, 'f', 1, 64)
}

func dinnerLabel(lang i18n.Lang, e evening) string {
	return i18n.WeekdayShort(lang, e.Dinner.Date) + " " + i18n.DateShort(lang, e.Dinner.Date) +
		" " + strconv.Itoa(e.Dinner.Date.Year())
}

// trend draws the people at every dinner, with the average as a dashed line.
func trend(lang i18n.Lang, in []evening) trendChart {
	c := trendChart{frame: newFrame()}
	if len(in) == 0 {
		c.Empty = true
		return c
	}
	top := 0
	sum := 0
	for _, e := range in {
		top = max(top, e.People())
		sum += e.People()
	}
	ymax, step := niceMax(top)
	c.YTicks = yTicks(c.frame, ymax, step)
	xs, w := slots(c.frame, len(in))
	c.XTicks = xTicks(xs, func(i int) string { return i18n.DateShort(lang, in[i].Dinner.Date) })
	c.Dots = len(in) <= 60

	points := make([]string, len(in))
	for i, e := range in {
		y := yFor(c.frame, float64(e.People()), ymax)
		points[i] = pt(xs[i], y)
		c.Cols = append(c.Cols, column{
			X: xs[i] - w/2, W: w, Guide: xs[i], DotY: y,
			Title: dinnerLabel(lang, e) + " — " + i18n.Count(lang, "person", e.People()),
		})
	}
	c.Line = strings.Join(points, " ")
	avg := float64(sum) / float64(len(in))
	c.AvgY = yFor(c.frame, avg, ymax)
	c.AvgLabel = i18n.T(lang, "stats.chart.average", decimal(lang, avg))
	return c
}

// stackParts are the bands of the stacked chart, from the bottom: the steady
// base first, the guests from outside the house on top.
var stackParts = []struct {
	class, key string
	value      func(evening) int
}{
	{"s1", "stats.part.standing", func(e evening) int { return e.Standing }},
	{"s2", "stats.part.booked", func(e evening) int { return e.Booked }},
	{"s3", "stats.part.brought", func(e evening) int { return e.Brought }},
	{"s4", "stats.part.external", func(e evening) int { return e.External }},
}

// stack draws every dinner's people as four stacked bands.
func stack(lang i18n.Lang, in []evening) stackChart {
	c := stackChart{frame: newFrame()}
	if len(in) == 0 {
		c.Empty = true
		return c
	}
	top := 0
	for _, e := range in {
		top = max(top, e.People())
	}
	ymax, step := niceMax(top)
	c.YTicks = yTicks(c.frame, ymax, step)
	xs, w := slots(c.frame, len(in))
	c.XTicks = xTicks(xs, func(i int) string { return i18n.DateShort(lang, in[i].Dinner.Date) })

	// A single dinner has no line to draw between two points, so it is drawn
	// as a band across the whole plot instead.
	edges := xs
	if len(in) == 1 {
		edges = []float64{c.Left, c.Right}
		in = []evening{in[0], in[0]}
	}

	below := make([]int, len(in))
	for _, part := range stackParts {
		var fwd, back []string
		above := make([]int, len(in))
		for i, e := range in {
			above[i] = below[i] + part.value(e)
			fwd = append(fwd, pt(edges[i], yFor(c.frame, float64(above[i]), ymax)))
		}
		for i := len(in) - 1; i >= 0; i-- {
			back = append(back, pt(edges[i], yFor(c.frame, float64(below[i]), ymax)))
		}
		l := layer{
			Class: part.class, Name: i18n.T(lang, part.key),
			Path: "M" + strings.Join(fwd, " L") + " L" + strings.Join(back, " L") + " Z",
		}
		last := len(in) - 1
		yTop := yFor(c.frame, float64(above[last]), ymax)
		yBottom := yFor(c.frame, float64(below[last]), ymax)
		if yBottom-yTop >= 16 {
			l.LabelY = (yTop+yBottom)/2 + 4
		}
		c.Layers = append(c.Layers, l)
		below = above
	}

	for i, e := range in[:len(xs)] {
		parts := make([]string, 0, len(stackParts))
		for _, part := range stackParts {
			parts = append(parts, i18n.T(lang, part.key)+" "+strconv.Itoa(part.value(e)))
		}
		c.Cols = append(c.Cols, column{
			X: xs[i] - w/2, W: w, Guide: xs[i],
			Title: dinnerLabel(lang, e) + " — " + i18n.Count(lang, "person", e.People()) +
				"\n" + strings.Join(parts, " · "),
		})
	}
	return c
}

// monthChart draws the meals served each month, across every season, for the
// last two years at most. The months between seasons are there too, at zero:
// a summer off is part of the story.
func monthChart(lang i18n.Lang, served []evening, now time.Time) barChart {
	c := barChart{frame: newFrame()}
	if len(served) == 0 {
		c.Empty = true
		return c
	}
	type month struct {
		at             time.Time
		meals, dinners int
	}
	first := served[0].Dinner.Date
	start := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, now.Location())
	end := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	if earliest := end.AddDate(0, -23, 0); start.Before(earliest) {
		start = earliest
	}
	var months []month
	at := map[string]int{}
	for m := start; !m.After(end); m = m.AddDate(0, 1, 0) {
		at[m.Format("2006-01")] = len(months)
		months = append(months, month{at: m})
	}
	for _, e := range served {
		if i, ok := at[e.Dinner.Key[:7]]; ok {
			months[i].meals += e.People()
			months[i].dinners++
		}
	}

	top := 0
	for _, m := range months {
		top = max(top, m.meals)
	}
	ymax, step := niceMax(top)
	c.YTicks = yTicks(c.frame, ymax, step)
	xs, w := slots(c.frame, len(months))
	c.XTicks = xTicks(xs, func(i int) string {
		m := months[i].at
		if i == 0 || m.Month() == time.January {
			return i18n.MonthShort(lang, m) + " " + strconv.Itoa(m.Year())
		}
		return i18n.MonthShort(lang, m)
	})
	// A 2px gap between neighbours, and never wider than a comfortable bar.
	bw := math.Min(w-2, 36)
	for i, m := range months {
		title := i18n.TitleCase(i18n.Month(lang, m.at)) + " " + strconv.Itoa(m.at.Year()) +
			" — " + i18n.T(lang, "stats.chart.meals", i18n.Count(lang, "meal", m.meals), i18n.Count(lang, "dinner", m.dinners))
		c.Cols = append(c.Cols, column{X: xs[i] - w/2, W: w, Guide: xs[i], Title: title})
		if m.meals == 0 {
			continue
		}
		y := yFor(c.frame, float64(m.meals), ymax)
		c.Bars = append(c.Bars, bar{
			X: xs[i] - bw/2, W: bw, Title: title,
			Path: topRounded(xs[i]-bw/2, y, bw, c.Bottom-y, 4),
		})
	}
	return c
}

// topRounded is a bar standing on the baseline with only its data end
// rounded.
func topRounded(x, y, w, h, r float64) string {
	r = math.Min(r, math.Min(w/2, h))
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	return "M" + f(x) + "," + f(y+h) +
		" V" + f(y+r) +
		" Q" + f(x) + "," + f(y) + " " + f(x+r) + "," + f(y) +
		" H" + f(x+w-r) +
		" Q" + f(x+w) + "," + f(y) + " " + f(x+w) + "," + f(y+r) +
		" V" + f(y+h) + " Z"
}
