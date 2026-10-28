// Package recap builds the weekly recap from the stream history and posts it every Sunday evening.
//
// A recap covers the seven days that end at the moment it is due (Sunday 20:00 WIB), so every stream belongs to exactly one recap.
package recap

import (
	"sort"
	"strings"
	"time"

	"github.com/Nadif243/fancord-bot/internal/history"
	"github.com/Nadif243/fancord-bot/internal/msg"
)

// WIB is Western Indonesia Time. Indonesia has no daylight saving, so a fixed offset is exact, and it does not depend on the computer having a timezone database (Windows often does not).
var WIB = time.FixedZone("WIB", 7*60*60)

const (
	dueWeekday = time.Sunday
	dueHour    = 20 // 20:00 WIB
	window     = 7 * 24 * time.Hour
)

// LastDue is the most recent recap moment at or before now: the latest Sunday 20:00 WIB.
func LastDue(now time.Time) time.Time {
	t := now.In(WIB)
	d := time.Date(t.Year(), t.Month(), t.Day(), dueHour, 0, 0, 0, WIB)
	d = d.AddDate(0, 0, -int(t.Weekday()-dueWeekday))
	if d.After(t) { // it is Sunday, but before 20:00
		d = d.AddDate(0, 0, -7)
	}
	return d
}

// Matcher says who was in a stream. *collabs.Service satisfies it: the partners named in the title plus those added by hand, minus those removed by hand.
type Matcher interface {
	// Partners returns the display names of the partners who were in the stream.
	Partners(videoID, title string) []string
	// Unknown returns @handles in the title that the partner list does not know.
	Unknown(title string) []string
}

// Build turns the streams that ended in [from, to) into what the recap says.
// partners may be nil: the recap then has no collab line.
func Build(entries []history.Entry, from, to time.Time, partners Matcher) msg.RecapWeek {
	w := msg.RecapWeek{From: from.In(WIB), To: to.In(WIB)}

	for _, e := range entries {
		start := e.Start
		if start.IsZero() {
			start = e.End // we never learnt the start; this at least gives a day
		}
		w.Streams = append(w.Streams, msg.RecapStream{
			Title:    e.Title,
			URL:      "https://www.youtube.com/watch?v=" + e.VideoID,
			Start:    start.In(WIB),
			Duration: e.Duration(),
		})
	}
	sort.SliceStable(w.Streams, func(i, j int) bool { return w.Streams[i].Start.Before(w.Streams[j].Start) })

	for _, st := range w.Streams {
		w.Total += st.Duration
	}

	// With one stream (or one day) "longest" and "busiest" say nothing new, so those lines are left out.
	if len(w.Streams) >= 2 {
		w.Longest = longest(w.Streams)
		w.Busiest = busiestDay(w.Streams)
	}
	w.Collabs = countCollabs(entries, partners)
	return w
}

// countCollabs counts, for every partner who was in a stream, the streams they were in. Most streams first; a tie goes to the name that comes first alphabetically.
func countCollabs(entries []history.Entry, m Matcher) []msg.RecapCollab {
	if m == nil {
		return nil
	}
	count := map[string]int{}
	for _, e := range entries {
		for _, name := range m.Partners(e.VideoID, e.Title) { // each name once per stream
			count[name]++
		}
	}
	var out []msg.RecapCollab
	for name, n := range count {
		out = append(out, msg.RecapCollab{Name: name, Streams: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Streams != out[j].Streams {
			return out[i].Streams > out[j].Streams
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// UnknownHandles lists the @handles in these streams' titles that the partner list does not know, each once, oldest stream first.
func UnknownHandles(entries []history.Entry, m Matcher) []string {
	if m == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		for _, h := range m.Unknown(e.Title) {
			if k := strings.ToLower(h); !seen[k] {
				seen[k] = true
				out = append(out, h)
			}
		}
	}
	return out
}

// longest is the stream that ran the longest; the earlier one wins a tie.
// It is nil when no duration is known.
func longest(streams []msg.RecapStream) *msg.RecapStream {
	var best *msg.RecapStream
	for i := range streams {
		if streams[i].Duration > 0 && (best == nil || streams[i].Duration > best.Duration) {
			best = &streams[i]
		}
	}
	return best
}

// busiestDay is the calendar day (WIB, by start time) with the most time on air. A tie goes to the day with more streams, then to the earlier day. It is nil when everything happened on one day.
func busiestDay(streams []msg.RecapStream) *msg.RecapDay {
	type key struct {
		y int
		m time.Month
		d int
	}
	days := map[key]*msg.RecapDay{}
	var order []key // first-seen order = oldest first, because streams are sorted
	for _, st := range streams {
		y, m, d := st.Start.Date()
		k := key{y, m, d}
		day, ok := days[k]
		if !ok {
			day = &msg.RecapDay{Day: time.Date(y, m, d, 0, 0, 0, 0, WIB)}
			days[k] = day
			order = append(order, k)
		}
		day.Count++
		day.Total += st.Duration
	}
	if len(days) < 2 {
		return nil
	}
	var best *msg.RecapDay
	for _, k := range order {
		d := days[k]
		if best == nil || d.Total > best.Total || (d.Total == best.Total && d.Count > best.Count) {
			best = d
		}
	}
	return best
}
