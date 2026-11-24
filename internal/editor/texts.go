package editor

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

const (
	secondsPerMinute = 60
	secondsPerHour   = 60 * secondsPerMinute
	// maxCaptionRunes of the trainer's caption shown in the description: the whole caption is kept in the task, the
	// description stays within the Telegram caption of a video (1024 characters).
	maxCaptionRunes = 300
	// noCutSeconds: less than this cut is "nothing to cut".
	noCutSeconds = 0.5
	// maxTitleLines of the exercise titles listed in the description; the rest is counted.
	maxTitleLines = 8
)

// Texts for the trainer. A failure is shown after «Не получилось выполнить задачу.» (tasks).
const (
	noSourceText = "Не нашёл видео в задаче. Пришлите исходник в «Исходники» ещё раз."
	// editFailedText: a temporary failure after all the retries of the task.
	editFailedText = "Монтаж не удался из-за технической ошибки, я уже пробовал несколько раз. " +
		"Пришлите исходник ещё раз чуть позже."
	// technicalText: the setup is broken (no key, the key refused, no brand fonts, no worker); the team is told in
	// «Техничка», the trainer gets no technical details.
	technicalText = "Монтаж сейчас недоступен из-за технической неполадки — я уже передал её технической команде. " +
		"Пришлите исходник ещё раз чуть позже."
	// otherFailureText: a failure without a text of its own.
	otherFailureText   = "Не получилось смонтировать это видео. Пришлите исходник ещё раз чуть позже."
	sourceMissingText  = "Не нашёл исходник в хранилище. Пришлите видео ещё раз."
	sourceTooLargeText = "Исходник слишком большой, такой я не скачиваю. Сожмите видео или разрежьте его на части."
	badSourceText      = "Не смог прочитать видео. Пришлите его ещё раз — лучше исходным файлом из галереи."
	noAudioText        = "В видео нет звука, а монтирую я по речи. Пришлите видео со звуком."
	noSpeechText       = "Не нашёл в видео речи, по которой можно резать. Пришлите видео, где вы говорите в кадре."

	// cannotRevisionText and unclearRevisionText: a revision the editor cannot do or did not understand (shown after
	// «Не получилось применить правку.» above the previous draft).
	// The note ends with «Можно одобрить эту версию или дать правку ещё раз.», so these texts do not ask for it again.
	cannotRevisionText = "Такое я пока не умею: могу обрезать, убрать или вернуть фрагмент, выбрать дубль, поправить " +
		"слово в субтитрах, подписать упражнения и убрать звук."
	unclearRevisionText = "Не понял, что именно поменять. Пример правки: „убери первые 3 секунды“ или " +
		"„оставь второй дубль“."
	// modelFailedText: the decision failed for a technical reason after all the retries of the task.
	modelFailedText = "Не получилось решить, как смонтировать видео, из-за технической ошибки — я уже пробовал " +
		"несколько раз. Пришлите исходник ещё раз чуть позже."
	// The revision's variants of the technical texts: the source is there, the trainer only gives the revision again.
	modelFailedRevisionText = "Не смог её разобрать из-за технической ошибки, я уже пробовал несколько раз — " +
		"попробуйте чуть позже."
	editFailedRevisionText = "Монтаж новой версии не удался из-за технической ошибки, я уже пробовал несколько раз — " +
		"попробуйте чуть позже."
	technicalRevisionText = "Мешает техническая неполадка — я уже передал её технической команде, попробуйте чуть позже."
	// captionFailedNote: the caption was not applied because the decision failed on our side.
	captionFailedNote = "Подпись как указание к монтажу не применил из-за технической ошибки. Если нужна правка, " +
		"ответьте на черновик."
	// artifactsMissingText: the transcript or the plan of the earlier edit is gone from the storage.
	artifactsMissingText = "Не нашёл материалы прошлого монтажа этого видео. Пришлите исходник ещё раз — " +
		"смонтирую заново."
	// captionCannotNote and captionUnclearNote: the first draft is made without the caption's instruction.
	captionCannotNote = "Подпись как указание к монтажу не применил: такое я пока не умею. Могу обрезать, убрать или " +
		"вернуть фрагмент, выбрать дубль, поправить слово в субтитрах, подписать упражнения и убрать звук — ответьте " +
		"на черновик правкой."
	captionUnclearNote = "Подпись как указание к монтажу не применил: не понял, что именно поменять. Если нужна " +
		"правка, ответьте на черновик, например: „убери первые 3 секунды“."
	// workoutFallbackNote: a source with little speech, the decision failed on our side: the whole video is the draft.
	workoutFallbackNote = "Смонтировал как тренировку, ничего не вырезая: разобрать подпись и речь не получилось из-за " +
		"технической ошибки. Обрезать, подписать упражнения или убрать звук можно правкой — ответьте на черновик."
	// titlesHint: a workout without exercise titles.
	titlesHint = "Подписи упражнений можно добавить правкой с временем, например: „подпиши выпады с 0:15 до 0:45“."
	// titlesHeader goes before the list of the exercise titles of a workout.
	titlesHeader = "Подписи упражнений:"
)

// forVersion is the text of the first version or of a revision.
func forVersion(version int, first, revision string) string {
	if version > 1 {
		return revision
	}

	return first
}

// failureText is the trainer's reason of a failed job, by its code (technical codes get technicalText before).
func (e *Executor) failureText(r *editjob.Result) string {
	switch r.Code {
	case editjob.SourceMissingCode:
		return sourceMissingText
	case editjob.SourceTooLargeCode:
		return sourceTooLargeText
	case editjob.SourceTooLongCode:
		return tooLongText(r.SourceDuration, r.MaxDuration)
	case editjob.BadSourceCode:
		return badSourceText
	case editjob.NoAudioCode:
		return noAudioText
	case editjob.NoSpeechCode:
		return noSpeechText
	case editjob.TimeoutCode:
		return runningTimeoutText(e.jobTimeout)
	case editjob.QueueTimeoutCode:
		return queueTimeoutText(e.queueTimeout)
	default:
		return otherFailureText
	}
}

// techReportText is the report in «Техничка»: the task and the code, the details are in the logs.
func techReportText(taskID int64, code string) string {
	return fmt.Sprintf("Монтажёр: задача %d не выполнена, код %s. Подробности — в логах app и media-worker.", taskID, code)
}

// tooLongText: «Исходник длится 12:30, а монтирую я видео до 10 мин. Разрежьте его на части и пришлите по одной.»
func tooLongText(duration, limit float64) string {
	return fmt.Sprintf("Исходник длится %s, а монтирую я видео до %s. Разрежьте его на части и пришлите по одной.",
		clock(duration), minutesText(limit))
}

// runningTimeoutText: the edit ran longer than EDITOR_JOB_TIMEOUT after a worker took it — the video is too heavy.
func runningTimeoutText(timeout time.Duration) string {
	return fmt.Sprintf("Монтаж этого видео занял больше %s и был остановлен — скорее всего, оно слишком длинное. "+
		"Разрежьте его на части и пришлите по одной.", minutesText(timeout.Seconds()))
}

// queueTimeoutText: no worker took the job within EDITOR_QUEUE_TIMEOUT — the queue was too long, not the video.
func queueTimeoutText(timeout time.Duration) string {
	return fmt.Sprintf("Очередь на монтаж была слишком длинной: за %s до этого видео дело не дошло. "+
		"Пришлите исходник ещё раз чуть позже.", minutesText(timeout.Seconds()))
}

// Describe is the description of the video draft. The first version: the durations before and after, what the
// editor changed by the caption and decided on the disputed repeats (summary), the trainer's caption. A revision: the
// durations of the previous version and this one and what was changed. summary is nil for an automatic cut.
func Describe(r *editjob.Result, version int, caption string, summary *editjob.Summary) string {
	if version > 1 {
		return describeRevision(r, summary)
	}

	if summary != nil && summary.Mode == roughcut.WorkoutMode {
		return describeWorkout(r, caption, summary)
	}

	lines := make([]string, 0, 3)

	removed := r.SourceDuration - r.Duration
	if removed < noCutSeconds {
		lines = append(lines, fmt.Sprintf("Черновик рилс готов: %s, вырезать было почти нечего.", clock(r.Duration)))
	} else {
		lines = append(lines, fmt.Sprintf("Черновик рилс готов: было %s, стало %s — вырезал %s (паузы, оговорки "+
			"и неудачные дубли).", clock(r.SourceDuration), clock(r.Duration), clock(removed)))
	}

	switch {
	case summary != nil:
		lines = append(lines, summary.Changes...)
	case r.Candidates > 0:
		lines = append(lines, fmt.Sprintf("Спорные повторы оставил в ролике: %d.", r.Candidates))
	}

	if text := strings.TrimSpace(caption); text != "" {
		lines = append(lines, "Ваша подпись к исходнику: «"+truncate(text, maxCaptionRunes)+"»")
	}

	if summary != nil && summary.Note != "" {
		lines = append(lines, summary.Note)
	}

	return strings.Join(lines, "\n")
}

// describeRevision: «Новая версия рилс: было 0:45, стало 0:42.» and what changed.
func describeRevision(r *editjob.Result, summary *editjob.Summary) string {
	if summary == nil {
		summary = &editjob.Summary{}
	}

	header := fmt.Sprintf("Новая версия рилс: %s.", clock(r.Duration))
	if summary.PreviousDuration > 0 {
		header = fmt.Sprintf("Новая версия рилс: было %s, стало %s.", clock(summary.PreviousDuration), clock(r.Duration))
	}

	lines := append([]string{header}, summary.Changes...)
	if summary.Note != "" {
		lines = append(lines, summary.Note)
	}

	return strings.Join(lines, "\n")
}

// captionNotes say why the caption was not applied, by the decision.
var captionNotes = map[editplan.OutcomeKind]string{
	editplan.CannotOutcome: captionCannotNote,
	editplan.FailedOutcome: captionFailedNote,
}

// firstSummary is the summary of a decided first version: what the caption changed, how the disputed repeats were
// decided, and why the caption was not applied if it was not. A workout gets its mode, its exercise titles (or a hint
// how to add them) and, when the decision failed, a note that the whole video was kept.
func firstSummary(req *editplan.Request, outcome *editplan.Outcome) *editjob.Summary {
	candidates := len(req.Plan.Candidates)
	summary := &editjob.Summary{}

	workout := isWorkout(req.Plan)
	if outcome.Kind == editplan.EditOutcome {
		workout = outcome.Mode == roughcut.WorkoutMode
	}

	if workout {
		summary.Mode = roughcut.WorkoutMode
	}

	if outcome.Kind != editplan.EditOutcome {
		if candidates > 0 {
			summary.Changes = []string{fmt.Sprintf("Спорные повторы оставил в ролике: %d.", candidates)}
		}

		switch {
		case workout && outcome.Kind == editplan.FailedOutcome:
			summary.Note = workoutFallbackNote
		case strings.TrimSpace(req.Caption) != "":
			summary.Note = captionNotes[outcome.Kind]
			if summary.Note == "" {
				summary.Note = captionUnclearNote
			}
		}

		if workout && summary.Note != workoutFallbackNote {
			summary.Changes = append(summary.Changes, titlesHint)
		}

		return summary
	}

	summary.Changes = append(summary.Changes, outcome.Changes...)

	if candidates > 0 {
		summary.Changes = append(summary.Changes, fmt.Sprintf("Спорные повторы: вырезал %d, оставил %d.",
			outcome.Cut, candidates-outcome.Cut))
	}

	if workout {
		summary.Changes = append(summary.Changes, titleLines(outcome)...)
	}

	return summary
}

// isWorkout: the plan is in the workout mode (the worker planned a source with little speech so).
func isWorkout(plan *roughcut.Plan) bool {
	return plan.Edit != nil && plan.Edit.Mode == roughcut.WorkoutMode
}

// titleLines list the exercise titles of a workout with their time (at most maxTitleLines), name the exercises the
// editor could not place in time, or, without titles, say how to add them.
func titleLines(outcome *editplan.Outcome) []string {
	var lines []string

	if len(outcome.Titles) > 0 {
		lines = append(lines, titlesHeader)

		shown := outcome.Titles
		if len(shown) > maxTitleLines {
			shown = shown[:maxTitleLines-1]
		}

		lines = append(lines, shown...)

		if rest := len(outcome.Titles) - len(shown); rest > 0 {
			lines = append(lines, fmt.Sprintf("И ещё подписей: %d.", rest))
		}
	}

	if len(outcome.Untimed) > 0 {
		names := make([]string, 0, len(outcome.Untimed))
		for _, name := range outcome.Untimed {
			names = append(names, "«"+name+"»")
		}

		return append(lines, untimedNote(names))
	}

	if len(outcome.Titles) == 0 {
		lines = append(lines, titlesHint)
	}

	return lines
}

// untimedNote: «Не поставил подписи «Выпады», «Планка»: не понял, в какой момент их показать. …»
func untimedNote(names []string) string {
	return "Не поставил подписи " + strings.Join(names, ", ") + ": не понял, в какой момент их показать. " +
		"Добавьте их правкой с временем, например: „подпиши выпады с 0:15 до 0:45“."
}

// describeWorkout is the description of the first version of a workout: the whole video or what was cut, the changes,
// the titles, the caption and the note.
func describeWorkout(r *editjob.Result, caption string, summary *editjob.Summary) string {
	header := fmt.Sprintf("Черновик тренировки готов: %s — ролик целиком, паузы и молчаливые отрезки не вырезал.",
		clock(r.Duration))

	if removed := r.SourceDuration - r.Duration; removed >= noCutSeconds {
		header = fmt.Sprintf("Черновик тренировки готов: было %s, стало %s — паузы и молчаливые отрезки оставил, "+
			"убрал только указанное (%s).", clock(r.SourceDuration), clock(r.Duration), clock(removed))
	}

	lines := append([]string{header}, summary.Changes...)

	if text := strings.TrimSpace(caption); text != "" {
		lines = append(lines, "Ваша подпись к исходнику: «"+truncate(text, maxCaptionRunes)+"»")
	}

	if summary.Note != "" {
		lines = append(lines, summary.Note)
	}

	return strings.Join(lines, "\n")
}

// longReelNote: the draft is longer than Instagram publishes as a reel; it is not refused, the trainer decides.
func longReelNote(limit float64) string {
	return fmt.Sprintf("Ролик длится дольше %s — Instagram может не опубликовать его как рилс. Если нужно короче, "+
		"ответьте на черновик, например: „обрежь до %s“.", minutesText(limit), minutesText(limit))
}

// clock formats seconds as m:ss.
func clock(seconds float64) string {
	total := int(math.Round(max(seconds, 0)))

	return fmt.Sprintf("%d:%02d", total/secondsPerMinute, total%secondsPerMinute)
}

// minutesText is a whole number of hours as «3 ч», of minutes as «10 мин» (no plural forms needed), any other
// duration as m:ss.
func minutesText(seconds float64) string {
	total := int(math.Round(seconds))

	switch {
	case total > 0 && total%secondsPerHour == 0:
		return fmt.Sprintf("%d ч", total/secondsPerHour)
	case total%secondsPerMinute == 0:
		return fmt.Sprintf("%d мин", total/secondsPerMinute)
	default:
		return clock(seconds)
	}
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	return strings.TrimSpace(string(runes[:limit])) + "…"
}
