package editplan

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// A cleaned first version keeps at least minCleanupShare of the reel without the cleaning and at least
// minCleanupSeconds (shortCleanupShare of a reel shorter than twice that): the failed takes and the preparation are a
// part of the speech, not most of it; a reel cut shorter was shortened, not cleaned.
const (
	minCleanupShare   = 0.25
	minCleanupSeconds = 15.0
	shortCleanupShare = 0.5
)

// CleanupLinePrefix starts the line of the description about the cleaning of a first version (cleanupLine): the
// editor's description of the draft names the failed takes only when they were cut.
const CleanupLinePrefix = "Убрал неудачные дубли и подготовку к съёмке"

// cleaning is what the cleaning of a first version removed from the reel: the fragments of speech and their seconds.
type cleaning struct {
	fragments int
	seconds   float64
}

// cleaningTask is said after the task of a first version with a caption that is cleaned too.
const cleaningTask = " Ещё почисти ролик (раздел «Чистка дублей и подготовки»): неудачные дубли, повторы подряд и " +
	"подготовку к съёмке вырежи через cleanup_words, а то, о чём просит подпись, — как обычно."

// selectionCleaning is said in the task of the selection: the failed takes go first, through cleanup_words, and count
// in the length of the reel.
const selectionCleaning = " Сначала выкинь неудачные дубли, повторы подряд и подготовку к съёмке через cleanup_words " +
	"(раздел «Чистка дублей и подготовки»); из дублей оставляй последний полный. Туда же — короткие вставки и реакции " +
	"отдельной репликой («не переживай», «ну что», ругательство). Вырезанное через cleanup_words тоже сокращает ролик " +
	"и считается в его длине."

// cleanupTask is the task of a first version only cleaned by the editor.
func cleanupTask(req *Request) string {
	task := "Задача: первый монтаж рилс. Подписи нет — почисти ролик (раздел «Чистка дублей и подготовки»): убери " +
		"через cleanup_words неудачные дубли, повторы подряд и подготовку к съёмке; содержательные фразы не сокращай."

	if req.Hook {
		task += " Предложи заголовок-хук."
	}

	if len(req.Plan.Candidates) > 0 {
		task += " Реши спорные повторы."
	}

	return task + "\n"
}

// checkCleanup checks what a cleaning may do. cleanup_words are for a first talking head cleaned or shortened by the
// selection (Request.CleansTakes) only: a revision and a workout cut through remove_words. A first
// version only cleaned (Request.CleanupOnly) refuses what is not a cleaning — a length, cuts by time, exercise titles,
// muting: the trainer asked for nothing; the workout mode passes, but then nothing is cut. Cutting words, trimming,
// the disputed repeats, the hook, the screen notes and the corrections of the subtitles pass.
func checkCleanup(req *Request, in *Input) error {
	workout := in.Mode == roughcut.WorkoutMode

	if len(in.CleanupWords) > 0 && (!req.CleansTakes() || workout) {
		return checkFailedf("cleanup_words — только для чистки или редакторского отбора первой версии разговорного " +
			"ролика; иначе вырезай через remove_words")
	}

	if !req.CleanupOnly() {
		return nil
	}

	if notCleaning(in) {
		return checkFailedf("подписи нет, тренер ни о чём не просила: только чистка (раздел «Чистка дублей и " +
			"подготовки») — remove_words или cleanup_words, trim_start_seconds, trim_end_seconds, candidates, " +
			"hook_title, screen_notes; без target_seconds, cut_seconds, подписей упражнений и звука")
	}

	if workout && cutsWords(in) {
		return checkFailedf("в тренировке без подписи ничего не вырезай и не обрезай: режим workout можно выбрать, " +
			"но паузы и молчание — это упражнения")
	}

	return nil
}

// notCleaning: the input asks for what a cleaning without a caption may not do.
func notCleaning(in *Input) bool {
	return in.TargetSeconds > 0 || len(in.CutSeconds) > 0 || len(in.AddTitles) > 0 || (in.Mute != nil && *in.Mute)
}

// cutsWords: the input cuts words or trims the reel.
func cutsWords(in *Input) bool {
	return len(in.Remove) > 0 || in.TrimStart > 0 || in.TrimEnd > 0
}

// cleanupOf counts what the cleaning removed: the runs of words it cut that the reel had without it, and the seconds
// the reel lost. The cleaning is cleanup_words; a first version only cleaned counts remove_words and the trim too, and
// must keep enough of the reel without the cleaning (the disputed repeats decided). The selection has no such floor:
// the range of its length is the guard.
func cleanupOf(req *Request, in *Input, trimmed *trim, edit *roughcut.Edit, plan *roughcut.Plan) (cleaning, error) {
	only := req.CleanupOnly()
	uncut, baseTrim, cut := *in, trimmed.ranges, in.CleanupWords

	uncut.CleanupWords = nil

	if only {
		uncut.Remove, baseTrim, cut = nil, nil, slices.Concat(trimmed.ranges, in.Remove, in.CleanupWords)
	}

	base, err := mergeEdit(req, &uncut, baseTrim)
	if err != nil {
		return cleaning{}, err
	}

	base.Mode, base.CutTime = edit.Mode, edit.CutTime

	whole, err := roughcut.BuildPlanEdited(req.Transcript, req.Plan.SourceDuration, req.Plan.Settings, &base)
	if err != nil {
		return cleaning{}, fmt.Errorf("failed to build the plan without the cleaning: %w", err)
	}

	if floor := cleanupFloor(whole.Duration); only && plan.Duration < floor {
		return cleaning{}, checkFailedf("после чистки от ролика остаётся %.1f с из %.1f с, а должно остаться хотя бы "+
			"%.1f с: вырезай только неудачные дубли, повторы подряд и подготовку к съёмке, содержательные фразы оставь",
			plan.Duration, whole.Duration, floor)
	}

	words := req.Transcript.Words
	marked := make([]bool, len(words))

	for _, r := range cut {
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

// cleanupFloor is the shortest reel a cleaning may leave of a reel of the duration.
func cleanupFloor(duration float64) float64 {
	return max(duration*minCleanupShare, min(minCleanupSeconds, duration*shortCleanupShare))
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

	cut := in.CleanupWords
	if req.CleanupOnly() {
		cut = slices.Concat(in.Remove, in.CleanupWords)
	}

	out := make([]string, 0, len(cut))
	for _, r := range cut {
		out = append(out, fmt.Sprintf("Чистка: вырезаны слова %d–%d «%s»", r.From, r.To,
			strings.TrimSpace(rangeText(req.Transcript.Words, r))))
	}

	return out
}
