package roughcut

import (
	"fmt"
	"math"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	// Margins kept around words at every cut, so the attack and the tail of consonants are not clipped.
	defaultPadBefore = 0.08
	defaultPadAfter  = 0.12
	// boundaryGap is the pause that starts a new phrase for retake detection.
	boundaryGap = 0.3
	// A segment starts at the first word whose middle is no earlier than this before the segment start.
	segmentStartSlack = 0.05

	unitFiller = "м" // a filler, but also "метров" after a number

	msPerSecond = 1000
	minCut      = 0.001 // shorter differences are rounding noise, not cuts
)

// BuildPlan makes the edit decision list from a transcript: fillers and failed takes are removed,
// pauses longer than settings.MaxPause are cut down to the margins around words. duration is the length
// of the source; 0 means unknown (the plan then ends after the last word). It is deterministic and does
// no I/O.
func BuildPlan(transcript *transcription.Result, duration float64, settings Settings) Plan {
	plan, _ := BuildPlanAccepting(transcript, duration, settings, nil) // no IDs, so no error

	return plan
}

// BuildPlanAccepting is BuildPlan with the candidates of the given IDs cut as well: the editor (agent or
// person) only chooses among Plan.Candidates, the plan is rebuilt here with the same margins and rules.
func BuildPlanAccepting(
	transcript *transcription.Result,
	duration float64,
	settings Settings,
	accept []int,
) (Plan, error) {
	return BuildPlanEdited(transcript, duration, settings, &Edit{Accept: accept})
}

// BuildPlanEdited is BuildPlan with the editor's changes: the accepted candidates and the removed words are cut,
// the restored words are kept. Failed takes and candidates are found before the changes, so the IDs of the
// candidates are the same as in the automatic plan. Edit.Words and Edit.Notes do not change the plan, and the
// plan's Edit is left to the caller. A word index outside the transcript or an unknown candidate is
// ErrInvalidPlan.
func BuildPlanEdited(
	transcript *transcription.Result,
	duration float64,
	settings Settings,
	edit *Edit,
) (Plan, error) {
	settings = effectiveSettings(settings)
	words := transcript.Words
	duration = planDuration(transcript, duration, settings)

	texts := alignTokens(transcript)
	fillers := markFillers(words, newFillerSet(settings.Fillers))
	ph := &phrases{
		words:  words,
		texts:  texts,
		tokens: buildTokens(words, fillers, segmentStarts(words, transcript.Segments), texts),
	}
	// Retakes and candidates are found the same way in both modes, so the IDs of the candidates do not depend on it.
	retakes, candidates := detectRetakes(ph.tokens, settings.RetakeSimilarity)

	accepted, rest, err := splitAccepted(candidates, edit.Accept)
	if err != nil {
		return Plan{}, err
	}

	mode := edit.Mode.Effective()
	cuts := &cutPlan{mode: mode, words: words, duration: duration, settings: settings,
		times: normalizeIntervals(edit.CutTime, duration)}

	// A workout keeps the fillers and the failed takes: only what the editor chose is cut.
	automatic := markRetakes(fillers, ph.tokens, retakes)
	talkKeep, _ := buildIntervals(words, automatic, duration, settings)

	removed, takes := make([]CutReason, len(words)), []Take{}
	if mode == TalkMode {
		removed, takes = automatic, describeTakes(ph, retakes)
	}

	for i := range accepted {
		removed = markRetakes(removed, ph.tokens, []retake{accepted[i].retake})
	}

	removed, err = applyWordEdits(removed, edit)
	if err != nil {
		return Plan{}, err
	}

	keep, planCuts := cuts.build(removed)

	cutFor := func(c *candidate) Interval {
		_, withCut := cuts.build(markRetakes(removed, ph.tokens, []retake{c.retake}))

		return cutCovering(withCut, mid(words[ph.tokens[c.from].idx]))
	}

	return Plan{
		Version:        PlanVersion,
		SourceDuration: roundTime(duration),
		Duration:       totalDuration(keep),
		Settings:       settings,
		Keep:           keep,
		Cuts:           planCuts,
		Takes:          append(takes, describeAccepted(ph, accepted)...),
		Candidates:     describeCandidates(ph, rest, cutFor),
		Mode:           planMode(edit.Mode),
		SpeechShare:    speechShare(talkKeep, duration),
	}, nil
}

// speechShareDigits: the share is kept to thousandths.
const speechShareDigits = 1000

// speechShare is the share of the source the kept intervals cover, 0 for a source of unknown length.
func speechShare(keep []Interval, duration float64) float64 {
	if duration <= 0 {
		return 0
	}

	return math.Round(min(totalDuration(keep)/duration, 1)*speechShareDigits) / speechShareDigits
}

// planMode is the mode written into the plan: empty for a talking-head plan without an explicit mode, so the
// automatic plan stays as it was.
func planMode(m Mode) Mode {
	if m == "" {
		return ""
	}

	return m.Effective()
}

// applyWordEdits returns a copy of removed with the restored words kept and the removed words cut (a word in both
// is cut).
func applyWordEdits(removed []CutReason, edit *Edit) ([]CutReason, error) {
	out := append([]CutReason(nil), removed...)

	for _, r := range edit.Restore {
		if err := checkWordRange(r, len(out)); err != nil {
			return nil, err
		}

		for i := r.From; i <= r.To; i++ {
			out[i] = ""
		}
	}

	for _, r := range edit.Remove {
		if err := checkWordRange(r, len(out)); err != nil {
			return nil, err
		}

		for i := r.From; i <= r.To; i++ {
			out[i] = EditCutReason
		}
	}

	return out, nil
}

// checkWordRange: both ends within the words, From not after To.
func checkWordRange(r WordRange, words int) error {
	if r.From < 0 || r.To < r.From || r.To >= words {
		return fmt.Errorf("failed to edit words %d–%d of %d: %w", r.From, r.To, words, ErrInvalidPlan)
	}

	return nil
}

// planDuration is the source duration, or the transcript's, or the end of the last word.
func planDuration(transcript *transcription.Result, duration float64, settings Settings) float64 {
	if duration <= 0 {
		duration = transcript.Duration
	}

	if words := transcript.Words; duration <= 0 && len(words) > 0 {
		duration = words[len(words)-1].End + settings.PadAfter
	}

	return duration
}

// splitAccepted separates the candidates the editor accepted; an unknown ID is an error.
func splitAccepted(candidates []candidate, accept []int) (accepted, rest []candidate, err error) {
	chosen := make(map[int]bool, len(accept))

	for _, id := range accept {
		if id < 0 || id >= len(candidates) {
			return nil, nil, fmt.Errorf("failed to accept candidate %d of %d: %w", id, len(candidates), ErrInvalidPlan)
		}

		chosen[id] = true
	}

	for _, c := range candidates {
		if chosen[c.id] {
			accepted = append(accepted, c)
		} else {
			rest = append(rest, c)
		}
	}

	return accepted, rest, nil
}

// cutCovering returns the cut that holds t, or an empty interval at t.
func cutCovering(cuts []Cut, t float64) Interval {
	for _, c := range cuts {
		if t >= c.Start && t < c.End {
			return Interval{Start: c.Start, End: c.End}
		}
	}

	return Interval{Start: roundTime(t), End: roundTime(t)}
}

// effectiveSettings shrinks the margins in proportion when they do not fit into MaxPause: the silence
// left at a cut is PadAfter + PadBefore and must not be longer than the longest allowed pause.
func effectiveSettings(s Settings) Settings {
	out := s
	out.Fillers = append([]string(nil), s.Fillers...)

	if out.PadBefore < 0 {
		out.PadBefore = 0
	}

	if out.PadAfter < 0 {
		out.PadAfter = 0
	}

	if pads := out.PadBefore + out.PadAfter; pads > out.MaxPause && pads > 0 {
		k := max(out.MaxPause, 0) / pads
		out.PadBefore = roundTime(out.PadBefore * k)
		out.PadAfter = roundTime(out.PadAfter * k)
	}

	return out
}

// markFillers marks the fillers to cut. "м" after a number is a unit ("400 м"), not a hum.
func markFillers(words []transcription.Word, fillers fillerSet) []CutReason {
	removed := make([]CutReason, len(words))
	prev := ""

	for i, w := range words {
		norm := normalizeWord(w.Word)
		if fillers.has(norm) && (norm != unitFiller || !isNumeral(prev)) {
			removed[i] = FillerCutReason
		}

		prev = norm
	}

	return removed
}

// segmentStarts marks the words that open a Whisper segment.
func segmentStarts(words []transcription.Word, segments []transcription.Segment) map[int]bool {
	starts := make(map[int]bool, len(segments))
	w := 0

	for _, s := range segments {
		for w < len(words) && (words[w].Start+words[w].End)/2 < s.Start-segmentStartSlack {
			w++
		}

		if w < len(words) && words[w].Start < s.End {
			starts[w] = true
		}
	}

	return starts
}

func buildTokens(
	words []transcription.Word,
	removed []CutReason,
	segStarts map[int]bool,
	texts []string,
) []token {
	tokens := make([]token, 0, len(words))
	prev := -1

	for i, w := range words {
		norm := normalizeWord(w.Word)
		if removed[i] != "" || norm == "" {
			continue
		}

		boundary := prev < 0 || segStarts[i] || w.Start-words[prev].End >= boundaryGap || hasRemoved(removed, prev+1, i)
		tokens = append(tokens, token{
			idx: i, norm: norm, start: w.Start, end: w.End, boundary: boundary,
			sentenceEnd: endsSentence(texts[i]), openEnd: endsOpen(texts[i]),
		})
		prev = i
	}

	return tokens
}

func hasRemoved(removed []CutReason, from, to int) bool {
	for i := from; i < to; i++ {
		if removed[i] != "" {
			return true
		}
	}

	return false
}

// markRetakes returns a copy of removed with every word of the failed attempts marked, fillers inside
// them included.
func markRetakes(removed []CutReason, tokens []token, retakes []retake) []CutReason {
	out := append([]CutReason(nil), removed...)

	for _, r := range retakes {
		for i := tokens[r.from].idx; i < tokens[r.to].idx; i++ {
			out[i] = RetakeCutReason
		}
	}

	return out
}

// intervalBuilder walks the words once and collects kept intervals and the cuts between them.
type intervalBuilder struct {
	words    []transcription.Word
	settings Settings
	keep     []Interval
	cuts     []Cut
	cur      Interval
	open     bool
	prev     int   // last kept word
	pending  []int // removed words since the last kept one
}

func buildIntervals(
	words []transcription.Word,
	removed []CutReason,
	duration float64,
	settings Settings,
) ([]Interval, []Cut) {
	b := &intervalBuilder{words: words, settings: settings, prev: -1}

	for i := range words {
		if removed[i] != "" {
			b.pending = append(b.pending, i)

			continue
		}

		b.addWord(i, removed)
	}

	b.finish(duration, removed)

	return b.keep, b.cuts
}

func (b *intervalBuilder) addWord(i int, reasons []CutReason) {
	w := b.words[i]

	if !b.open {
		start := max(w.Start-b.settings.PadBefore, 0)
		if len(b.pending) > 0 {
			start = min(max(start, b.words[b.pending[len(b.pending)-1]].End), w.Start)
		}

		b.cut(0, start, reasons)
		b.cur, b.open, b.prev = Interval{Start: start, End: w.End}, true, i

		return
	}

	prev := b.words[b.prev]
	end, next := prev.End+b.settings.PadAfter, w.Start-b.settings.PadBefore

	switch {
	case len(b.pending) > 0:
		end = max(min(end, b.words[b.pending[0]].Start), prev.End)
		next = min(max(next, b.words[b.pending[len(b.pending)-1]].End), w.Start)
	case w.Start-prev.End <= b.settings.MaxPause:
		next = end // a short pause stays as it is
	}

	if next > end {
		b.cur.End = max(b.cur.End, end)
		b.keep = append(b.keep, b.cur)
		b.cut(b.cur.End, next, reasons)
		b.cur = Interval{Start: next, End: w.End}
	}

	b.cur.End = max(b.cur.End, w.End)
	b.prev = i
	b.pending = b.pending[:0]
}

func (b *intervalBuilder) finish(duration float64, reasons []CutReason) {
	if !b.open {
		b.cut(0, duration, reasons)

		return
	}

	end := b.words[b.prev].End + b.settings.PadAfter
	if len(b.pending) > 0 {
		end = max(min(end, b.words[b.pending[0]].Start), b.words[b.prev].End)
	}

	b.cur.End = min(max(b.cur.End, end), max(duration, b.words[b.prev].End))
	b.keep = append(b.keep, b.cur)
	b.cut(b.cur.End, duration, reasons)

	b.keep = roundIntervals(b.keep)
}

// cut records [from, to) as removed: the reason of the removed words in it, or a pause if there are none.
// It clears the pending words.
func (b *intervalBuilder) cut(from, to float64, reasons []CutReason) {
	defer func() { b.pending = b.pending[:0] }()

	if to-from < minCut {
		return
	}

	reason := PauseCutReason
	texts := make([]string, 0, len(b.pending))

	for _, i := range b.pending {
		if reasons[i] == RetakeCutReason || reason == PauseCutReason {
			reason = reasons[i]
		}

		texts = append(texts, strings.TrimSpace(b.words[i].Word))
	}

	b.cuts = append(b.cuts, Cut{
		Start: roundTime(from), End: roundTime(to), Reason: reason, Text: strings.Join(texts, " "),
	})
}

func roundIntervals(intervals []Interval) []Interval {
	out := make([]Interval, 0, len(intervals))

	for _, iv := range intervals {
		r := Interval{Start: roundTime(iv.Start), End: roundTime(iv.End)}
		if r.End-r.Start >= minCut {
			out = append(out, r)
		}
	}

	return out
}

func totalDuration(keep []Interval) float64 {
	total := 0.0
	for _, iv := range keep {
		total += iv.End - iv.Start
	}

	return roundTime(total)
}

// roundTime drops float noise (12.300000000001 -> 12.3) and keeps milliseconds.
func roundTime(seconds float64) float64 {
	return math.Round(seconds*msPerSecond) / msPerSecond
}
