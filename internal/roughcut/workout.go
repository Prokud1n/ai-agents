package roughcut

import (
	"cmp"
	"slices"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// MinTitleSeconds: a title left shorter than this on the reel (most of its time was cut) is not shown.
const MinTitleSeconds = 0.5

// cutPlan builds the kept intervals and the cuts of a plan from the removed words, in the plan's mode.
type cutPlan struct {
	mode     Mode
	words    []transcription.Word
	duration float64
	settings Settings
	// times are the editor's cuts by time (Edit.CutTime): sorted, merged and within the source.
	times []Interval
	// grid places the cut points of a talking head by the sound level; nil: by the margins.
	grid *cutGrid
}

// build: a talking head is cut at the pauses and the removed words, a workout only at the removed words; the cuts by
// time are applied in both modes.
func (c *cutPlan) build(removed []CutReason) ([]Interval, []Cut) {
	if c.mode == WorkoutMode {
		return c.workout(removed)
	}

	keep, cuts := buildIntervals(c.words, removed, c.duration, c.settings, c.grid)

	return c.cutTimes(keep, cuts)
}

// workout keeps the whole source but the removed words (with the margins, up to the neighbouring kept words) and the
// cuts by time: pauses and silent stretches stay, the exercises are seen whole.
func (c *cutPlan) workout(removed []CutReason) ([]Interval, []Cut) {
	removals := c.wordRemovals(removed)
	for _, iv := range c.times {
		removals = append(removals, Cut{Start: iv.Start, End: iv.End, Reason: EditCutReason})
	}

	merged := mergeCuts(removals)
	keep := make([]Interval, 0, len(merged)+1)
	cuts := make([]Cut, 0, len(merged))
	from := 0.0

	for _, cut := range merged {
		if cut.Start > from {
			keep = append(keep, Interval{Start: from, End: cut.Start})
		}

		cuts = append(cuts, Cut{
			Start: roundTime(cut.Start), End: roundTime(cut.End), Reason: cut.Reason, Text: c.wordsIn(cut.Start, cut.End),
		})
		from = max(from, cut.End)
	}

	if c.duration > from {
		keep = append(keep, Interval{Start: from, End: c.duration})
	}

	return roundIntervals(keep), cuts
}

// wordRemovals are the runs of removed words with the margins around them, up to the kept words next to them.
func (c *cutPlan) wordRemovals(removed []CutReason) []Cut {
	var out []Cut

	for i := 0; i < len(removed); i++ {
		if removed[i] == "" {
			continue
		}

		from, reason := i, removed[i]

		for i+1 < len(removed) && removed[i+1] != "" {
			i++
			if removed[i] == RetakeCutReason {
				reason = RetakeCutReason
			}
		}

		prevEnd, nextStart := 0.0, c.duration
		if from > 0 {
			prevEnd = c.words[from-1].End
		}

		if i+1 < len(c.words) {
			nextStart = c.words[i+1].Start
		}

		out = append(out, Cut{
			Start:  max(c.words[from].Start-c.settings.PadBefore, prevEnd, 0),
			End:    min(c.words[i].End+c.settings.PadAfter, nextStart, c.duration),
			Reason: reason,
		})
	}

	return out
}

// cutTimes removes the editor's cuts by time from the kept intervals of a talking-head plan: what of them was kept
// becomes cuts of the edit.
func (c *cutPlan) cutTimes(keep []Interval, cuts []Cut) ([]Interval, []Cut) {
	if len(c.times) == 0 {
		return keep, cuts
	}

	out := slices.Clone(cuts)

	for _, t := range c.times {
		for _, iv := range keep {
			if from, to := max(iv.Start, t.Start), min(iv.End, t.End); to-from >= minCut {
				out = append(out, Cut{
					Start: roundTime(from), End: roundTime(to), Reason: EditCutReason, Text: c.wordsIn(from, to),
				})
			}
		}
	}

	slices.SortStableFunc(out, func(a, b Cut) int { return cmp.Compare(a.Start, b.Start) })

	return roundIntervals(subtractIntervals(keep, c.times)), out
}

// wordsIn joins the words whose middle is within [from, to).
func (c *cutPlan) wordsIn(from, to float64) string {
	var parts []string

	for _, w := range c.words {
		if m := mid(w); m >= from && m < to {
			parts = append(parts, strings.TrimSpace(w.Word))
		}
	}

	return strings.Join(parts, " ")
}

// mergeCuts sorts the cuts and joins the overlapping and touching ones; a join with a failed take keeps that reason.
// Cuts shorter than minCut are dropped.
func mergeCuts(cuts []Cut) []Cut {
	sorted := slices.Clone(cuts)
	slices.SortStableFunc(sorted, func(a, b Cut) int { return cmp.Compare(a.Start, b.Start) })

	out := make([]Cut, 0, len(sorted))

	for _, cut := range sorted {
		if cut.End-cut.Start < minCut {
			continue
		}

		n := len(out)
		if n == 0 || cut.Start > out[n-1].End {
			out = append(out, cut)

			continue
		}

		out[n-1].End = max(out[n-1].End, cut.End)
		if cut.Reason == RetakeCutReason {
			out[n-1].Reason = RetakeCutReason
		}
	}

	return out
}

// normalizeIntervals clips the intervals to [0, duration], sorts and merges them; nil when nothing is left.
func normalizeIntervals(intervals []Interval, duration float64) []Interval {
	cuts := make([]Cut, 0, len(intervals))
	for _, iv := range intervals {
		cuts = append(cuts, Cut{Start: max(iv.Start, 0), End: min(iv.End, duration)})
	}

	merged := mergeCuts(cuts)
	if len(merged) == 0 {
		return nil
	}

	out := make([]Interval, 0, len(merged))
	for _, cut := range merged {
		out = append(out, Interval{Start: cut.Start, End: cut.End})
	}

	return out
}

// NormalizeIntervals is the editor's cuts by time as a plan keeps them: within the source, sorted and merged.
func NormalizeIntervals(intervals []Interval, duration float64) []Interval {
	out := roundIntervals(normalizeIntervals(intervals, duration))
	if len(out) == 0 {
		return nil
	}

	return out
}

// subtractIntervals returns keep without the sorted cuts.
func subtractIntervals(keep, cuts []Interval) []Interval {
	out := make([]Interval, 0, len(keep))

	for _, iv := range keep {
		pieces := []Interval{iv}
		for _, cut := range cuts {
			pieces = subtractOne(pieces, cut)
		}

		out = append(out, pieces...)
	}

	return out
}

// subtractOne removes the cut from each piece.
func subtractOne(pieces []Interval, cut Interval) []Interval {
	out := make([]Interval, 0, len(pieces)+1)

	for _, p := range pieces {
		if cut.End <= p.Start || cut.Start >= p.End {
			out = append(out, p)

			continue
		}

		if cut.Start > p.Start {
			out = append(out, Interval{Start: p.Start, End: cut.Start})
		}

		if cut.End < p.End {
			out = append(out, Interval{Start: cut.End, End: p.End})
		}
	}

	return out
}

// RemapTitles puts the titles of an edit onto the timeline of the cut by keep (see TitleSpans): ordered by time,
// without the titles that are not shown, every end set.
func RemapTitles(titles []Title, keep []Interval) []Title {
	if len(titles) == 0 || len(keep) == 0 {
		return nil
	}

	sorted := slices.Clone(titles)
	slices.SortStableFunc(sorted, func(a, b Title) int { return cmp.Compare(a.Start, b.Start) })

	spans := TitleSpans(sorted, keep)
	out := make([]Title, 0, len(sorted))

	for i, t := range sorted {
		if spans[i].End > spans[i].Start {
			out = append(out, Title{Text: t.Text, Start: spans[i].Start, End: spans[i].End})
		}
	}

	return out
}

// TitleSpans are the times of the titles on the reel cut by keep, one per title in the order given: a title runs from
// its start to its end, but not past the next title in time (without an end — up to the next title, the last one to
// the end of the reel); what of it was cut is skipped, and a title that starts in a cut starts where the reel goes on.
// The span of a title left shorter than MinTitleSeconds is empty.
func TitleSpans(titles []Title, keep []Interval) []Interval {
	spans := make([]Interval, len(titles))
	if len(keep) == 0 {
		return spans
	}

	order := make([]int, len(titles))
	for i := range order {
		order[i] = i
	}

	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(titles[a].Start, titles[b].Start) })

	sourceEnd := keep[len(keep)-1].End

	for k, i := range order {
		end := sourceEnd
		if k+1 < len(order) {
			end = titles[order[k+1]].Start
		}

		if t := titles[i]; t.End > t.Start {
			end = min(end, t.End)
		}

		start, stop := ReelTime(keep, titles[i].Start), ReelTime(keep, end)
		if stop-start >= MinTitleSeconds {
			spans[i] = Interval{Start: roundTime(start), End: roundTime(stop)}
		}
	}

	return spans
}

// ReelTime is the moment of the reel cut by keep that shows the source time t; a time inside a cut is where the reel
// goes on after it.
func ReelTime(keep []Interval, t float64) float64 {
	offset := 0.0

	for _, iv := range keep {
		if t < iv.End {
			return offset + max(t-iv.Start, 0)
		}

		offset += iv.End - iv.Start
	}

	return offset
}

// SourceTime is the time of the source shown at the moment t of the reel cut by keep (the end of the last kept
// interval for t at or past the end of the reel; t itself without keep).
func SourceTime(keep []Interval, t float64) float64 {
	if len(keep) == 0 {
		return t
	}

	offset := 0.0

	for _, iv := range keep {
		length := iv.End - iv.Start
		if t < offset+length {
			return iv.Start + max(t-offset, 0)
		}

		offset += length
	}

	return keep[len(keep)-1].End
}

// SourceIntervals are the fragments of the source that the stretch [from, to) of the reel cut by keep shows.
func SourceIntervals(keep []Interval, from, to float64) []Interval {
	var out []Interval

	offset := 0.0

	for _, iv := range keep {
		length := iv.End - iv.Start
		if s, e := max(from, offset), min(to, offset+length); e > s {
			out = append(out, Interval{Start: iv.Start + s - offset, End: iv.Start + e - offset})
		}

		offset += length
	}

	return out
}
