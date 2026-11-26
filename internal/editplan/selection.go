package editplan

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	// lengthTolerance of the reel's length around the target: 40 s may come out 34–46 s, as whole phrases are cut.
	lengthTolerance = 0.15
	// minTargetSeconds: a shorter length is not a reel the trainer would ask for.
	minTargetSeconds = 5
	// minShortenSeconds: a decision that leaves the reel less than this shorter did not shorten it («не сокращай»).
	minShortenSeconds = 0.5
	// endWords quoted of the start and the end of a shortened reel in the description.
	endWords = 6
	// maxDroppedQuotes named in the description, maxDroppedRunes of each.
	maxDroppedQuotes = 3
	maxDroppedRunes  = 40
	ellipsisMark     = "…"
)

// selection is the editorial selection of a decision: whether phrases were picked for the length of a reel (and the
// reel got shorter), the length asked for (0: the recommended range), and the reel built with the decision.
//
// cleanup is the cleaning of a first talking head (Request.Cleaning), only that it is the whole decision (no caption:
// Request.CleanupOnly), cleaned what it removed.
type selection struct {
	on       bool
	target   float64
	duration float64
	keep     []roughcut.Interval
	cleanup  bool
	only     bool
	cleaned  cleaning
}

// newSelection is the selection of a decision with the plan it builds: on only when the reel got shorter.
func newSelection(req *Request, in *Input, mode roughcut.Mode, plan *roughcut.Plan) selection {
	baseline := baselineDuration(req)

	return selection{
		on:     isSelected(req, in, mode) && plan.Duration < baseline-minShortenSeconds,
		target: in.TargetSeconds, duration: estimatedDuration(req, plan.Duration, baseline), keep: plan.Keep,
		cleanup: req.Cleaning() && mode == roughcut.TalkMode, only: req.CleanupOnly() && mode == roughcut.TalkMode,
	}
}

// baselineDuration is the length of the reel before the decision counted as the plans of the decisions are counted
// here (roughcut.BuildPlanEdited, the margins around the words). The worker places the cut points of a talking head by
// the sound (T3.19b): its Plan.Duration differs from such a plan by up to a few seconds, so a plan built here compared
// with it would see a selection where none was made (or miss one). Plan.Duration if the plan cannot be rebuilt.
func baselineDuration(req *Request) float64 {
	previous := previousEdit(req.Plan)

	before, err := roughcut.BuildPlanEdited(req.Transcript, req.Plan.SourceDuration, req.Plan.Settings, &previous)
	if err != nil {
		return req.Plan.Duration
	}

	return before.Duration
}

// estimatedDuration is the length the worker's reel of a plan built here (duration, by the margins) will have: the
// worker cuts a talking head by the sound, its reels are shorter or longer by the same share as the plan the model saw
// (req.Plan.Duration) is against the same edit counted here (baseline). duration as it is without either.
func estimatedDuration(req *Request, duration, baseline float64) float64 {
	if baseline <= 0 || req.Plan.Duration <= 0 {
		return duration
	}

	return duration * req.Plan.Duration / baseline
}

// isSelected: a talking head shortened for a reel — a first version longer than the target, or a length asked for.
func isSelected(req *Request, in *Input, mode roughcut.Mode) bool {
	return mode == roughcut.TalkMode && (req.Selecting() || in.TargetSeconds > 0)
}

// checkTarget: the length asked for is a reel's length and fits into the source.
func checkTarget(req *Request, target float64) error {
	if target == 0 {
		return nil
	}

	if !validSeconds(target) || target < minTargetSeconds || target > req.Plan.SourceDuration {
		return checkFailedf("target_seconds — длина рилс в секундах от %d до длины исходника (%.1f с); "+
			"если тренер не называла длину, не указывай его", minTargetSeconds, req.Plan.SourceDuration)
	}

	return nil
}

// lengthBounds are the lengths the reel of the decision may have: around the length asked for (any mode), or the
// recommended range for a talking head shortened by the selection; false when the length is free.
func lengthBounds(req *Request, in *Input, mode roughcut.Mode) (low, high float64, ok bool) {
	switch {
	case in.TargetSeconds > 0:
		return in.TargetSeconds * (1 - lengthTolerance), in.TargetSeconds * (1 + lengthTolerance), true
	case mode == roughcut.TalkMode && req.Selecting():
		low, high = req.TargetRange()

		return low * (1 - lengthTolerance), high * (1 + lengthTolerance), true
	default:
		return 0, 0, false
	}
}

// buildSized builds the plan of the edit and checks the reel's length against lengthBounds.
func buildSized(req *Request, in *Input, mode roughcut.Mode, edit *roughcut.Edit) (roughcut.Plan, error) {
	plan, err := buildPlan(req, edit)
	if err != nil {
		return roughcut.Plan{}, err
	}

	low, high, ok := lengthBounds(req, in, mode)
	if !ok {
		return plan, nil
	}

	// The reel the worker renders, not the plan counted here by the margins.
	duration := estimatedDuration(req, plan.Duration, baselineDuration(req))
	if duration >= low && duration <= high {
		return plan, nil
	}

	hint := lengthHint(req, in, &plan, duration, low, high)

	if in.TargetSeconds > 0 {
		return roughcut.Plan{}, checkFailedf("после решения ролик длится %.1f с, а длина по просьбе тренера — %.0f с "+
			"(допустимо %.1f–%.1f с): %s", duration, in.TargetSeconds, low, high, hint)
	}

	from, to := req.TargetRange()

	return roughcut.Plan{}, checkFailedf("после решения ролик длится %.1f с, а рилс должен быть %.0f–%.0f с "+
		"(допустимо %.1f–%.1f с): %s; если тренер просила другую длину, укажи её в target_seconds", duration,
		from, to, low, high, hint)
}

// maxSizedHints are the phrases named in a length hint.
const maxSizedHints = 3

// sized is a run of words [from, to] that lasts seconds in the source.
type sized struct {
	from, to int
	seconds  float64
}

// lengthHint says the model of a reel out of its length how many seconds to cut or to bring back and which phrases
// are about that long, so the second attempt gets the length: kept phrases of a reel too long, the ranges the model
// cut of a reel too short. duration is the length of the reel (estimatedDuration).
func lengthHint(req *Request, in *Input, plan *roughcut.Plan, duration, low, high float64) string {
	words := req.Transcript.Words

	if duration > high {
		least, most := duration-high, duration-low

		return fmt.Sprintf("вырежи ещё от %.1f до %.1f с целыми фразами через remove_words%s", least, most,
			sizedHint("подходят по длине оставленные фразы", words, keptPhrases(req, plan), least, most))
	}

	least, most := low-duration, high-duration

	return fmt.Sprintf("ролик короче нужного: вырежи на %.1f–%.1f с меньше — не вырезай часть фраз из remove_words "+
		"(в правке — верни их через restore_words)%s", least, most,
		sizedHint("подходят по длине вырезанные тобой отрезки", words, removedRanges(req, in.Remove), least, most))
}

// keptPhrases are the phrases of the transcript (as the prompt lists them) left in the reel by the plan, with the
// seconds of the reel they take.
func keptPhrases(req *Request, plan *roughcut.Plan) []sized {
	words := req.Transcript.Words
	all := phrases(req.Transcript, plan)
	out := make([]sized, 0, len(all))

	for _, ph := range all {
		if ph.key.interval >= 0 {
			seconds := overlap(plan.Keep, words[ph.from].Start, words[ph.to].End)
			out = append(out, sized{from: ph.from, to: ph.to, seconds: seconds})
		}
	}

	return out
}

// removedRanges are the ranges of words cut by the decision with the seconds they take in the reel the decision
// starts from (bringing one back adds about that much).
func removedRanges(req *Request, cut []roughcut.WordRange) []sized {
	words := req.Transcript.Words
	out := make([]sized, 0, len(cut))

	for _, r := range cut {
		seconds := overlap(req.Plan.Keep, words[r.From].Start, words[r.To].End)
		out = append(out, sized{from: r.From, to: r.To, seconds: seconds})
	}

	return out
}

// overlap is how much of start–end the kept intervals cover.
func overlap(keep []roughcut.Interval, start, end float64) float64 {
	total := 0.0

	for _, iv := range keep {
		total += max(min(end, iv.End)-max(start, iv.Start), 0)
	}

	return total
}

// sizedHint names up to maxSizedHints runs that last least–most seconds or, without such, the closest ones; "" when
// there is nothing to name.
func sizedHint(label string, words []transcription.Word, runs []sized, least, most float64) string {
	if len(runs) == 0 {
		return ""
	}

	middle := (least + most) / 2
	picked := make([]sized, 0, len(runs))

	for _, r := range runs {
		if r.seconds >= least && r.seconds <= most {
			picked = append(picked, r)
		}
	}

	if len(picked) == 0 {
		label = "ближе всего по длине"
		picked = slices.Clone(runs)
		slices.SortStableFunc(picked, func(a, b sized) int {
			return cmp.Compare(math.Abs(a.seconds-middle), math.Abs(b.seconds-middle))
		})
	}

	names := make([]string, 0, maxSizedHints)
	for _, r := range picked[:min(len(picked), maxSizedHints)] {
		names = append(names, fmt.Sprintf("слова %s (%s) «%s»", indexRange(r.from, r.to), durationText(r.seconds),
			truncate(joinWords(words[r.from:r.to+1]), maxDroppedRunes)))
	}

	return "; " + label + ": " + strings.Join(names, ", ")
}

// selectionLines are the sentences for the trainer about a shortened reel: how it starts and ends, and what did not
// make it into the reel (instead of a line for every fragment cut).
func selectionLines(req *Request, in *Input, sel *selection) []string {
	words := req.Transcript.Words
	kept := keptWords(words, sel.keep)

	var lines []string

	switch {
	case len(kept) > 2*endWords:
		first, last := kept[:endWords], kept[len(kept)-endWords:]
		lines = append(lines, "Оставил главное: начало «"+joinIndexed(words, first)+ellipsisMark+"», финал «"+
			ellipsisMark+joinIndexed(words, last)+"».")
	case len(kept) > 0:
		lines = append(lines, "Оставил главное: «"+joinIndexed(words, kept)+"».")
	}

	if len(in.Remove) == 0 {
		return lines
	}

	names := make([]string, 0, min(len(in.Remove), maxDroppedQuotes))
	for _, r := range in.Remove[:min(len(in.Remove), maxDroppedQuotes)] {
		names = append(names, "«"+truncate(quote(words, r), maxDroppedRunes)+"»")
	}

	line := "Не вошло: " + strings.Join(names, ", ")
	if rest := len(in.Remove) - len(names); rest > 0 {
		line += fmt.Sprintf(" и ещё %d", rest)
	}

	return append(lines, line+".")
}

// keptWords are the indices of the words left in the reel.
func keptWords(words []transcription.Word, keep []roughcut.Interval) []int {
	kept := make([]int, 0, len(words))

	for i, w := range words {
		if inKeep(keep, (w.Start+w.End)/2) {
			kept = append(kept, i)
		}
	}

	return kept
}

// joinIndexed is the text of the words (endWords of them at most, so it is short).
func joinIndexed(words []transcription.Word, indices []int) string {
	parts := make([]string, 0, len(indices))
	for _, i := range indices {
		parts = append(parts, strings.TrimSpace(words[i].Word))
	}

	return strings.Join(parts, " ")
}

// keptSet marks the words left in the reel.
func keptSet(words []transcription.Word, keep []roughcut.Interval) []bool {
	kept := make([]bool, len(words))
	for _, i := range keptWords(words, keep) {
		kept[i] = true
	}

	return kept
}

// anyKept: a word of the range is left in the reel.
func anyKept(kept []bool, r roughcut.WordRange) bool {
	for i := max(r.From, 0); i <= r.To && i < len(kept); i++ {
		if kept[i] {
			return true
		}
	}

	return false
}

func inKeep(keep []roughcut.Interval, t float64) bool {
	for _, iv := range keep {
		if t >= iv.Start && t < iv.End {
			return true
		}
	}

	return false
}

// selectionNotes explain the selection in the decisions log (details, not decisions): the length of the reel and every
// phrase left out.
func selectionNotes(req *Request, in *Input, sel *selection) []string {
	if !sel.on {
		return nil
	}

	out := make([]string, 0, len(in.Remove)+1)

	if sel.target > 0 {
		out = append(out, fmt.Sprintf("Длина рилс: %s (просьба тренера — %.0f с)",
			roughcut.FormatTimecode(sel.duration), sel.target))
	} else {
		low, high := req.TargetRange()
		out = append(out, fmt.Sprintf("Длина рилс: %s (рекомендуется %.0f–%.0f с)",
			roughcut.FormatTimecode(sel.duration), low, high))
	}

	for _, r := range in.Remove {
		out = append(out, fmt.Sprintf("Не вошло в рилс: слова %d–%d «%s»", r.From, r.To,
			rangeText(req.Transcript.Words, r)))
	}

	return out
}
