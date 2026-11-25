package editor

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

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
	// maxTitleLines of the exercise titles listed in the description, maxUntimedNames of the exercises without a time:
	// the rest is counted, so the description stays within the 1024 characters of a video caption.
	maxTitleLines   = 5
	maxUntimedNames = 5
	// maxWorkoutCaptionRunes of the caption quoted in the description of a workout (its list of exercises is long).
	maxWorkoutCaptionRunes = 120
	// maxDescriptionUnits: the description is the caption of the video, at most 1024 UTF-16 units in Telegram; about
	// 100 are left for the link to the source the gateway adds.
	maxDescriptionUnits = 920
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
	cannotRevisionText = "Такое я пока не умею: могу обрезать, сократить до нужной длины, убрать или вернуть фрагмент, " +
		"выбрать дубль, поправить слово в субтитрах, подписать упражнения и убрать звук."
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
	captionCannotNote = "Подпись как указание к монтажу не применил: такое я пока не умею. Могу обрезать, сократить до " +
		"нужной длины, убрать или вернуть фрагмент, выбрать дубль, поправить слово в субтитрах, подписать упражнения и " +
		"убрать звук — ответьте на черновик правкой."
	captionUnclearNote = "Подпись как указание к монтажу не применил: не понял, что именно поменять. Если нужна " +
		"правка, ответьте на черновик, например: „убери первые 3 секунды“."
	// workoutFallbackNote: a source with little speech, the decision failed on our side: the whole video is the draft.
	workoutFallbackNote = "Смонтировал как тренировку, ничего не вырезая: разобрать подпись и речь не получилось из-за " +
		"технической ошибки. Обрезать, подписать упражнения или убрать звук можно правкой — ответьте на черновик."
	// titlesHint: a workout without exercise titles.
	titlesHint = "Подписи упражнений можно добавить правкой с временем, например: „подпиши выпады с 0:15 до 0:45“."
	// titlesHeader goes before the list of the exercise titles of a workout.
	titlesHeader = "Подписи упражнений:"
	// longFailedNote and longUnchosenNote: a talking head longer than a reel was only cleaned, not shortened — the
	// decision failed on our side, or the editor did not manage to pick the phrases (formatted with the range).
	longFailedNote = "Ролик длиннее рекомендованных для рилс %s: сократить его не получилось из-за технической " +
		"ошибки, поэтому вырезал только паузы, оговорки и неудачные дубли. Если нужно короче, ответьте на черновик, " +
		"например: „сделай %s“."
	longUnchosenNote = "Ролик длиннее рекомендованных для рилс %s: не смог выбрать, что оставить, поэтому вырезал только " +
		"паузы, оговорки и неудачные дубли. Если нужно короче, ответьте на черновик, например: „сделай %s“."
	// shortenedNote ends a description whose lists did not fit into the caption of the video.
	shortenedNote = "Описание сокращено: не всё поместилось в подпись к видео."
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
//
// The description is the caption of the video, cut by Telegram at 1024 characters, so what must not be lost goes
// right after the header: the notes (the length of the reel) and the summary's note; the lists and the quoted caption
// go last.
func Describe(r *editjob.Result, version int, caption string, summary *editjob.Summary, notes ...string) string {
	var (
		header string
		body   []string
		quote  string
	)

	switch {
	case version > 1:
		header, body = revisionHeader(r, summary), summaryChanges(summary)
	case summary != nil && summary.Mode == roughcut.WorkoutMode:
		header, body = workoutHeader(r), summary.Changes

		if !summary.CaptionApplied {
			quote = captionQuote(caption, maxWorkoutCaptionRunes)
		}
	default:
		header, body, quote = talkHeader(r, summary), talkBody(r, summary), captionQuote(caption, maxCaptionRunes)
	}

	head := append([]string{header}, notes...)
	if summary != nil && summary.Note != "" {
		head = append(head, summary.Note)
	}

	tail := slices.Clone(body)
	if quote != "" {
		tail = append(tail, quote)
	}

	return fitDescription(head, tail)
}

// fitDescription joins the lines within maxDescriptionUnits: the head (the header and the notes) always, the tail
// (changes, lists, the quoted caption) while it fits; what does not fit is replaced by shortenedNote.
func fitDescription(head, tail []string) string {
	text := strings.Join(head, "\n")
	reserve := utf16Len("\n" + shortenedNote)

	for i, line := range tail {
		room := maxDescriptionUnits
		if i+1 < len(tail) {
			room -= reserve // a line after this one may not fit: the note must
		}

		next := text + "\n" + line
		if utf16Len(next) > room {
			return text + "\n" + shortenedNote
		}

		text = next
	}

	return text
}

// utf16Len is the length of the text as Telegram counts it.
func utf16Len(text string) int {
	return len(utf16.Encode([]rune(text)))
}

// talkHeader: «Черновик рилс готов: было 3:02, стало 2:21 — вырезал 0:41 (…).»; a reel shortened by the selection
// says so.
func talkHeader(r *editjob.Result, summary *editjob.Summary) string {
	if summary != nil && summary.Selected {
		return fmt.Sprintf("Черновик рилс готов: было %s, стало %s — сократил под рилс: оставил главное, вырезал паузы, "+
			"оговорки и то, что не вошло.", clock(r.SourceDuration), clock(r.Duration))
	}

	removed := r.SourceDuration - r.Duration
	if removed < noCutSeconds {
		return fmt.Sprintf("Черновик рилс готов: %s, вырезать было почти нечего.", clock(r.Duration))
	}

	return fmt.Sprintf("Черновик рилс готов: было %s, стало %s — вырезал %s (паузы, оговорки и неудачные дубли).",
		clock(r.SourceDuration), clock(r.Duration), clock(removed))
}

// talkBody is what the editor changed, or how many disputed repeats an automatic cut left.
func talkBody(r *editjob.Result, summary *editjob.Summary) []string {
	switch {
	case summary != nil:
		return summary.Changes
	case r.Candidates > 0:
		return []string{fmt.Sprintf("Спорные повторы оставил в ролике: %d.", r.Candidates)}
	default:
		return nil
	}
}

// workoutHeader: the whole video, or what was cut of it.
func workoutHeader(r *editjob.Result) string {
	if removed := r.SourceDuration - r.Duration; removed >= noCutSeconds {
		return fmt.Sprintf("Черновик тренировки готов: было %s, стало %s — паузы и молчаливые отрезки оставил, "+
			"убрал только указанное (%s).", clock(r.SourceDuration), clock(r.Duration), clock(removed))
	}

	return fmt.Sprintf("Черновик тренировки готов: %s — ролик целиком, паузы и молчаливые отрезки не вырезал.",
		clock(r.Duration))
}

// revisionHeader: «Новая версия рилс: было 0:45, стало 0:42.»
func revisionHeader(r *editjob.Result, summary *editjob.Summary) string {
	if summary != nil && summary.PreviousDuration > 0 {
		return fmt.Sprintf("Новая версия рилс: было %s, стало %s.", clock(summary.PreviousDuration), clock(r.Duration))
	}

	return fmt.Sprintf("Новая версия рилс: %s.", clock(r.Duration))
}

func summaryChanges(summary *editjob.Summary) []string {
	if summary == nil {
		return nil
	}

	return summary.Changes
}

// captionQuote: «Ваша подпись к исходнику: «…»», cut to limit; "" without a caption.
func captionQuote(caption string, limit int) string {
	text := strings.TrimSpace(caption)
	if text == "" {
		return ""
	}

	return "Ваша подпись к исходнику: «" + truncate(text, limit) + "»"
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

		summary.Note = undecidedNote(req, outcome.Kind, workout)

		if workout && summary.Note != workoutFallbackNote {
			summary.Changes = append(summary.Changes, titlesHint)
		}

		return summary
	}

	summary.Selected = outcome.Selected && !workout
	summary.Changes = append(summary.Changes, outcome.Changes...)

	if candidates > 0 {
		summary.Changes = append(summary.Changes, fmt.Sprintf("Спорные повторы: вырезал %d, оставил %d.",
			outcome.Cut, candidates-outcome.Cut))
	}

	if workout {
		summary.Changes = append(summary.Changes, titleLines(outcome)...)
		summary.CaptionApplied = len(outcome.Titles) > 0 && strings.TrimSpace(req.Caption) != ""
	}

	return summary
}

// undecidedNote says why the first version is the automatic cut: the workout kept whole, the caption not applied, the
// long talking head only cleaned.
func undecidedNote(req *editplan.Request, kind editplan.OutcomeKind, workout bool) string {
	if workout && kind == editplan.FailedOutcome {
		return workoutFallbackNote
	}

	notes := make([]string, 0, 2)

	if strings.TrimSpace(req.Caption) != "" {
		note := captionNotes[kind]
		if note == "" {
			note = captionUnclearNote
		}

		notes = append(notes, note)
	}

	if req.Selecting() {
		low, high := req.TargetRange()
		format := longUnchosenNote

		if kind == editplan.FailedOutcome {
			format = longFailedNote
		}

		notes = append(notes, fmt.Sprintf(format, rangeText(low, high), secondsText(high*exampleShare)))
	}

	return strings.Join(notes, " ")
}

// exampleShare of the target in the example of a revision («сделай 45 секунд» for 60 s).
const exampleShare = 0.75

// rangeText: «30–60 с».
func rangeText(low, high float64) string {
	return fmt.Sprintf("%.0f–%.0f с", low, high)
}

// Russian plural forms of «секунда» by the last digits of the number.
const (
	pluralTens    = 10
	pluralHundred = 100
	teenFrom      = 11
	teenTo        = 14
	fewTo         = 4
)

// secondsText: «45 секунд», «21 секунду», «32 секунды» (whole seconds, the word agreed with the number).
func secondsText(seconds float64) string {
	n := int(math.Round(seconds))
	last, lastTwo := n%pluralTens, n%pluralHundred

	switch {
	case lastTwo >= teenFrom && lastTwo <= teenTo:
		return fmt.Sprintf("%d секунд", n)
	case last == 1:
		return fmt.Sprintf("%d секунду", n)
	case last >= 2 && last <= fewTo:
		return fmt.Sprintf("%d секунды", n)
	default:
		return fmt.Sprintf("%d секунд", n)
	}
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
		return append(lines, untimedNote(outcome.Untimed))
	}

	if len(outcome.Titles) == 0 {
		lines = append(lines, titlesHint)
	}

	return lines
}

// untimedNote: «Не поставил подписи «Выпады», «Планка»: не понял, в какой момент их показать. …»; at most
// maxUntimedNames are named, the rest is counted («… и ещё 3»).
func untimedNote(untimed []string) string {
	names := make([]string, 0, min(len(untimed), maxUntimedNames))
	for _, name := range untimed[:min(len(untimed), maxUntimedNames)] {
		names = append(names, "«"+name+"»")
	}

	list := strings.Join(names, ", ")
	if rest := len(untimed) - len(names); rest > 0 {
		list += fmt.Sprintf(" и ещё %d", rest)
	}

	return "Не поставил подписи " + list + ": не понял, в какой момент их показать. " +
		"Добавьте их правкой с временем, например: „подпиши выпады с 0:15 до 0:45“."
}

// longReelNote: the draft is longer than Instagram shows well as a reel; it is not refused, the trainer decides.
func longReelNote(limit float64) string {
	return fmt.Sprintf("Ролик длится дольше %s — такие рилсы Instagram почти не показывает тем, кто на вас не "+
		"подписан. Если нужно короче, ответьте на черновик, например: „обрежь до %s“.", minutesText(limit),
		minutesText(limit))
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
