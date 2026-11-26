package editplan

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// minCleanupShare: a cleaned first version keeps at least this share of the automatic cut. The failed takes and the
// preparation for the shooting are a part of the speech, not most of it: a reel cut shorter was shortened, not cleaned.
const minCleanupShare = 0.4

// CleanupLinePrefix starts the line of the description about the cleaning of a first version (cleanupLine): the
// editor's description of the draft names the failed takes only when they were cut.
const CleanupLinePrefix = "Убрал неудачные дубли и подготовку к съёмке"

// cleaning is what the cleaning of a first version removed from the reel: the fragments of speech and their seconds.
type cleaning struct {
	fragments int
	seconds   float64
}

// cleanupTask is the task of a first version only cleaned by the editor.
func cleanupTask(req *Request) string {
	task := "Задача: первый монтаж рилс. Подписи нет — почисти ролик (раздел «Чистка дублей и подготовки»): убери " +
		"неудачные дубли, повторы подряд и подготовку к съёмке; содержательные фразы не сокращай."

	if req.Hook {
		task += " Предложи заголовок-хук."
	}

	if len(req.Plan.Candidates) > 0 {
		task += " Реши спорные повторы."
	}

	return task + "\n"
}

// checkCleanupOnly refuses in a first version only cleaned (Request.CleanupOnly) what is not a cleaning: a length, cuts
// by time, the workout mode, exercise titles and muting — the trainer asked for nothing. Cutting words, trimming, the
// disputed repeats, the hook, the screen notes and the corrections of the subtitles pass.
func checkCleanupOnly(req *Request, in *Input) error {
	if !req.CleanupOnly() {
		return nil
	}

	if in.TargetSeconds > 0 || len(in.CutSeconds) > 0 || in.Mode == roughcut.WorkoutMode || len(in.AddTitles) > 0 ||
		(in.Mute != nil && *in.Mute) {
		return checkFailedf("подписи нет, тренер ни о чём не просила: только чистка (раздел «Чистка дублей и " +
			"подготовки») — remove_words, trim_start_seconds, trim_end_seconds, candidates, hook_title, screen_notes; " +
			"без target_seconds, cut_seconds, смены режима, подписей упражнений и звука")
	}

	return nil
}

// cleanupOf checks that the cleaning left at least minCleanupShare of the automatic cut and counts what it removed: the
// runs of words cut by the decision (the trim included) that the reel had without it, and the seconds the reel lost.
func cleanupOf(req *Request, in *Input, trimmed *trim, edit *roughcut.Edit, plan *roughcut.Plan) (cleaning, error) {
	before := req.Plan.Duration
	if plan.Duration < before*minCleanupShare {
		return cleaning{}, checkFailedf("после чистки от ролика остаётся %.1f с из %.1f с — меньше %.0f%%: вырезай "+
			"только неудачные дубли, повторы подряд и подготовку к съёмке, содержательные фразы оставь", plan.Duration,
			before, minCleanupShare*percent)
	}

	uncut := *in
	uncut.Remove = nil

	base, err := mergeEdit(req, &uncut, nil)
	if err != nil {
		return cleaning{}, err
	}

	base.Mode, base.CutTime = edit.Mode, edit.CutTime

	whole, err := roughcut.BuildPlanEdited(req.Transcript, req.Plan.SourceDuration, req.Plan.Settings, &base)
	if err != nil {
		return cleaning{}, fmt.Errorf("failed to build the plan without the cleaning: %w", err)
	}

	words := req.Transcript.Words
	marked := make([]bool, len(words))

	for _, r := range slices.Concat(trimmed.ranges, in.Remove) {
		mark(marked, r, true)
	}

	keptBefore, keptAfter := keptSet(words, whole.Keep), keptSet(words, plan.Keep)
	fragments, inside := 0, false

	for i := range words {
		if !keptBefore[i] {
			continue
		}

		cleaned := marked[i] && !keptAfter[i]
		if cleaned && !inside {
			fragments++
		}

		inside = cleaned
	}

	return cleaning{fragments: fragments, seconds: max(whole.Duration-plan.Duration, 0)}, nil
}

// cleanupLine: «Убрал неудачные дубли и подготовку к съёмке: 3 фрагмента (52,4 с).»; "" when nothing was removed.
func cleanupLine(c cleaning) string {
	if c.fragments == 0 {
		return ""
	}

	return fmt.Sprintf("%s: %s (%s).", CleanupLinePrefix, plural(c.fragments, "фрагмент", "фрагмента", "фрагментов"),
		durationText(c.seconds))
}

// cleanupNotes name every range of words the cleaning cut in the decisions log (details, not decisions).
func cleanupNotes(req *Request, in *Input, sel *selection) []string {
	if !sel.cleanup {
		return nil
	}

	out := make([]string, 0, len(in.Remove))
	for _, r := range in.Remove {
		out = append(out, fmt.Sprintf("Чистка: вырезаны слова %d–%d «%s»", r.From, r.To,
			strings.TrimSpace(rangeText(req.Transcript.Words, r))))
	}

	return out
}
