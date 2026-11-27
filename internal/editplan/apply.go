package editplan

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

const (
	// maxRanges of words cut and brought back by one decision, maxFixes of corrected words.
	maxRanges = 50
	maxFixes  = 30
	// maxWordRunes of a corrected subtitle word (a word or a short name, never a sentence).
	maxWordRunes = 40
	// maxReasonRunes of an explanation in the decisions log.
	maxReasonRunes = 300
	// minReelSeconds: a shorter reel is not a reel; such an edit is refused.
	minReelSeconds = 1.0
)

// Decision of submit_edit.
type Decision string

const (
	EditDecision    Decision = "edit"
	CannotDecision  Decision = "cannot"
	UnclearDecision Decision = "unclear"
)

// Input is the input of submit_edit: see inputSchema.
type Input struct {
	Decision   Decision             `json:"decision"`
	Reason     string               `json:"reason"`
	TrimStart  float64              `json:"trim_start_seconds"`
	TrimEnd    float64              `json:"trim_end_seconds"`
	Remove     []roughcut.WordRange `json:"remove_words"`
	Restore    []roughcut.WordRange `json:"restore_words"`
	Candidates []CandidateDecision  `json:"candidates"`
	FixWords   []FixInput           `json:"fix_words"`
	// Mode: talk or workout; empty keeps the mode of the previous version.
	Mode roughcut.Mode `json:"mode"`
	// CutSeconds are stretches of the timeline cut by time; RestoreCuts bring back the cuts by time of the
	// previous versions by their number in the prompt.
	CutSeconds  []SecondsRange `json:"cut_seconds"`
	RestoreCuts []int          `json:"restore_cuts"`
	// AddTitles, ChangeTitles and RemoveTitles edit the exercise titles; UntimedTitles are exercises of the caption
	// whose moment the editor could not find: they are named in the description, not drawn.
	AddTitles     []TitleInput  `json:"add_titles"`
	ChangeTitles  []TitleChange `json:"change_titles"`
	RemoveTitles  []int         `json:"remove_titles"`
	UntimedTitles []string      `json:"untimed_titles"`
	// Mute silences the source (true) or brings its sound back (false); nil keeps it.
	Mute *bool `json:"mute"`
	// Hide are words left out of the subtitles only (a phrase made up by the speech recognition on music): the video
	// and the sound stay.
	Hide []roughcut.WordRange `json:"hide_words"`
	// TargetSeconds is the length of the reel the trainer asked for («сделай 40 секунд»), 0 without one: the reel
	// built with the decision must be within lengthTolerance of it.
	TargetSeconds float64 `json:"target_seconds"`
	// HookTitle is the hook title over the first seconds: a text sets it, "" removes it, nil keeps it.
	HookTitle *string `json:"hook_title"`
	// ScreenNotes are the points and numbers on the screen of a talking head: a list replaces the previous version's
	// (empty removes them), nil keeps them.
	ScreenNotes *[]NoteInput `json:"screen_notes"`
	// Declined are the trainer's requests the editor does not do (music, color...), by kind: the trainer gets the
	// prepared text of each, with the rest of the edit or with a refusal.
	Declined []DeclinedKind `json:"declined"`
	// CleanupWords are the failed takes, a phrase said again and the preparation for the shooting the cleaning of a
	// first version cuts (Request.Cleaning): cut like Remove, counted in one line of the description.
	CleanupWords []roughcut.WordRange `json:"cleanup_words"`

	// dropHidden: the screen notes the reel would not show are left out instead of failing the check (the last
	// attempt of a decision that fails only by them, Session); never from the model's JSON.
	dropHidden bool
}

// FixInput corrects the subtitle word Index to Text; Word is the word the model means as it reads it in the
// transcript: a word that is not there is a wrong index (a word counted wrong inside a phrase).
type FixInput struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
	Word  string `json:"word,omitempty"`
}

// CandidateDecision decides a disputed repeat: Cut removes its earlier phrase (the repeat stays), otherwise both stay.
type CandidateDecision struct {
	ID     int    `json:"id"`
	Cut    bool   `json:"cut"`
	Reason string `json:"reason"`
}

// CheckError is an edit that failed the checks; Message goes back to the model.
type CheckError struct {
	Message string

	// hiddenNotes: the decision failed because screen notes would not be seen in the reel (placeNotes).
	hiddenNotes bool
}

func (e *CheckError) Error() string {
	return "edit check failed: " + e.Message
}

func checkFailedf(format string, args ...any) error {
	return &CheckError{Message: fmt.Sprintf(format, args...)}
}

// wordState is a word's change by the editor, over all the versions.
type wordState uint8

const (
	unchangedWord wordState = iota
	removedWord
	restoredWord
)

// Apply checks the model's input against the request and returns the decision: for an edit, the whole edit of the
// new version (the previous edit with this change), checked by building the plan with it. A *CheckError is the
// reason for the model; any other error is a broken request.
//
// A revision that changes nothing but declines a request (only «добавь музыку») is a refusal with the declined texts.
func Apply(req *Request, in *Input) (*Outcome, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}

	reason := oneLine(in.Reason)

	if err := checkDeclined(req, in.Declined); err != nil {
		return nil, err
	}

	declined := declinedLines(in.Declined)

	switch in.Decision {
	case CannotDecision:
		return &Outcome{Kind: CannotOutcome, Reason: reason, Declined: declined}, nil
	case UnclearDecision:
		return &Outcome{Kind: UnclearOutcome, Reason: reason, Declined: declined}, nil
	case EditDecision:
	default:
		return nil, checkFailedf("decision должен быть edit, cannot или unclear, а не %q", in.Decision)
	}

	outcome, err := applyEdit(req, in, reason)

	switch {
	case errors.Is(err, errNothingChanged) && len(declined) > 0:
		return &Outcome{Kind: CannotOutcome, Reason: reason, Declined: declined}, nil
	case err != nil:
		return nil, err
	}

	outcome.Declined = declined

	return outcome, nil
}

// applyEdit checks and builds the edit decision.
func applyEdit(req *Request, in *Input, reason string) (*Outcome, error) {
	if err := checkInput(req, in); err != nil {
		return nil, err
	}

	mode := nextMode(req, in)
	trimmed := trimFor(req, in, mode)

	edit, err := mergeEdit(req, in, trimmed.ranges)
	if err != nil {
		return nil, err
	}

	news, err := mergeExtras(req, in, mode, &trimmed, &edit)
	if err != nil {
		return nil, err
	}

	plan, err := buildSized(req, in, mode, &edit)
	if err != nil {
		return nil, err
	}

	placed, hidden := placeNotes(req, in, edit.ScreenNotes, &plan)
	if len(hidden) > 0 && !in.dropHidden {
		return nil, hiddenNotesError(req, in, hidden)
	}

	edit.ScreenNotes = placed

	sel := newSelection(req, in, mode, &plan)
	sel.notes, sel.hidden = edit.ScreenNotes, hidden
	if sel.cleanup {
		if sel.cleaned, err = cleanupOf(req, in, &trimmed, &edit, &plan); err != nil {
			return nil, err
		}
	}

	edit.Notes, edit.Details = notes(req, in, reason, &sel), details(req, in, &sel)
	edit.Details = append(edit.Details, hiddenNotesDetails(hidden)...)
	ending := checkEnding(req, in, &edit, &sel)
	tail := checkPhraseEnds(req, in, &edit, &plan)
	count := checkCounts(req, &edit, &plan, &sel)
	fragment := checkFragments(req, &edit, &plan)

	shown, err := checkTitlesShown(&edit, &plan)
	if err != nil {
		return nil, err
	}

	if err := checkNotesApart(in, &edit, &plan); err != nil {
		return nil, err
	}

	if req.IsRevision() {
		if err := checkChanged(req, &edit, &plan); err != nil {
			return nil, err
		}
	}

	cut, kept := countCandidates(in.Candidates)

	return &Outcome{
		Kind: EditOutcome, Edit: edit, Cut: cut, Kept: kept,
		Changes:  describeChanges(req, in, &trimmed, &placedTitles{newTitles: news, keep: plan.Keep}, &sel),
		Duration: plan.Duration, Reason: reason, Mode: mode, Titles: titleLines(shown), Untimed: untimedTitles(in),
		Selected: sel.on, Target: in.TargetSeconds, Cleaned: sel.cleaned.fragments,
		CleanedTakes: sel.cleaned.takes, endingHint: ending, tailHint: tail, countHint: count, fragmentHint: fragment,
		hidden: hidden,
	}, nil
}

// checkInput checks the limits and that every index and ID exists.
func checkInput(req *Request, in *Input) error {
	words := len(req.Transcript.Words)

	switch {
	case len(in.Remove)+len(in.Restore)+len(in.Hide)+len(in.CleanupWords) > maxRanges:
		return checkFailedf("слишком много отрезков: не больше %d в remove_words, restore_words, hide_words и "+
			"cleanup_words вместе", maxRanges)
	case len(in.FixWords) > maxFixes:
		return checkFailedf("слишком много исправлений: не больше %d в fix_words", maxFixes)
	case !validSeconds(in.TrimStart) || !validSeconds(in.TrimEnd):
		return checkFailedf("trim_start_seconds и trim_end_seconds — неотрицательные числа секунд")
	case in.TrimStart+in.TrimEnd >= timelineDuration(req):
		return checkFailedf("обрезка %.1f + %.1f с не меньше всего видео (%.1f с)", in.TrimStart, in.TrimEnd,
			timelineDuration(req))
	}

	if err := checkRanges(words, in.Remove, in.Restore, in.Hide, in.CleanupWords); err != nil {
		return err
	}

	if err := checkCandidates(req.Plan, in.Candidates); err != nil {
		return err
	}

	if err := checkWorkoutInput(req, in); err != nil {
		return err
	}

	if err := checkFixes(req, in.FixWords); err != nil {
		return err
	}

	if err := checkHook(in.HookTitle); err != nil {
		return err
	}

	if err := checkNotesInput(req, in); err != nil {
		return err
	}

	if err := checkCleanup(req, in); err != nil {
		return err
	}

	if err := checkHookOnly(req, in); err != nil {
		return err
	}

	return checkTarget(req, in.TargetSeconds)
}

// checkRanges: every range of words is within the transcript, from not above to.
func checkRanges(words int, lists ...[]roughcut.WordRange) error {
	for _, ranges := range lists {
		for _, r := range ranges {
			if r.From < 0 || r.To < r.From || r.To >= words {
				return checkFailedf("отрезок слов %d–%d вне расшифровки: индексы от 0 до %d, from не больше to",
					r.From, r.To, words-1)
			}
		}
	}

	return nil
}

func validSeconds(s float64) bool {
	return s >= 0 && !math.IsNaN(s) && !math.IsInf(s, 0)
}

func checkCandidates(plan *roughcut.Plan, decisions []CandidateDecision) error {
	known := make(map[int]bool, len(plan.Candidates))
	for i := range plan.Candidates {
		known[plan.Candidates[i].ID] = true
	}

	seen := make(map[int]bool, len(decisions))

	for _, d := range decisions {
		if !known[d.ID] {
			return checkFailedf("спорного повтора с id %d нет в списке", d.ID)
		}

		if seen[d.ID] {
			return checkFailedf("спорный повтор %d решён дважды", d.ID)
		}

		seen[d.ID] = true
	}

	return nil
}

// checkFixes: every corrected word exists, is the word the model means (Word), once, fits and changes the word as it is
// now (the transcript or an earlier correction).
func checkFixes(req *Request, fixes []FixInput) error {
	words := len(req.Transcript.Words)
	seen := make(map[int]bool, len(fixes))

	for _, f := range fixes {
		text := strings.TrimSpace(f.Text)

		switch {
		case f.Index < 0 || f.Index >= words:
			return checkFailedf("слова с индексом %d нет в расшифровке", f.Index)
		case !sameWord(req, f.Index, f.Word):
			return checkFailedf("слово %d в расшифровке — «%s», а не «%s»: пересчитай индекс от начала фразы",
				f.Index, strings.TrimSpace(req.Transcript.Words[f.Index].Word), oneLine(f.Word))
		case seen[f.Index]:
			return checkFailedf("слово %d исправлено дважды", f.Index)
		case text == "" || utf8.RuneCountInString(text) > maxWordRunes:
			return checkFailedf("исправление слова %d: от 1 до %d символов", f.Index, maxWordRunes)
		case strings.ContainsFunc(text, forbiddenRune):
			return checkFailedf("исправление слова %d: без переносов строк, невидимых символов и { } \\ < >", f.Index)
		case text == currentWord(req, f.Index):
			return checkFailedf("исправление слова %d ничего не меняет: в субтитрах уже «%s»", f.Index, text)
		}

		seen[f.Index] = true
	}

	return nil
}

// sameWord: the model's reading of the word (empty: not given) is the word of the transcript or of the subtitles,
// whatever the case and the punctuation around it.
func sameWord(req *Request, index int, word string) bool {
	if strings.TrimSpace(word) == "" {
		return true
	}

	normalize := func(s string) string { return strings.ToLower(strings.TrimFunc(s, unicode.IsPunct)) }
	want := normalize(strings.TrimSpace(word))

	return want == normalize(strings.TrimSpace(req.Transcript.Words[index].Word)) ||
		want == normalize(currentWord(req, index))
}

// currentWord is the word as the subtitles show it now: an earlier correction, or the transcript.
func currentWord(req *Request, index int) string {
	previous := previousEdit(req.Plan)
	for _, f := range previous.Words {
		if f.Index == index {
			return strings.TrimSpace(f.Text)
		}
	}

	return strings.TrimSpace(req.Transcript.Words[index].Word)
}

// forbiddenRune: control characters and line breaks, invisible format characters (zero width, bidi marks), and
// what subtitles or tags could read as markup.
func forbiddenRune(r rune) bool {
	return invisibleRune(r) || strings.ContainsRune(`{}\<>`, r)
}

func invisibleRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

// trim is what a trim of the start and the end cuts: the words (a talking head) or the fragments of the source (a
// workout, trimTime), and the seconds actually cut at the start and at the end of the timeline (for words: up to the
// end of the last word cut, from the start of the first one).
type trim struct {
	ranges     []roughcut.WordRange
	cuts       []roughcut.Interval
	start, end float64
}

// span is a word on a timeline; ok is false for a word that is not on it (cut from the reel).
type span struct {
	start, end float64
	ok         bool
}

// trimFor is the trim of the decision: by words, or by time in a workout (the exercise before the first word goes too).
func trimFor(req *Request, in *Input, mode roughcut.Mode) trim {
	if mode == roughcut.WorkoutMode {
		return trimTime(req, in.TrimStart, in.TrimEnd)
	}

	return trimWords(req, in.TrimStart, in.TrimEnd)
}

// trimWords: a word is trimmed when its middle is within the first trimStart or the last trimEnd seconds. The first
// version counts on the source (the trainer captioned the source), a revision on the reel the trainer saw.
func trimWords(req *Request, trimStart, trimEnd float64) trim {
	if trimStart <= 0 && trimEnd <= 0 {
		return trim{}
	}

	spans, total := timeline(req)
	marked := make([]bool, len(spans))

	var t trim

	for i, sp := range spans {
		if !sp.ok {
			continue
		}

		middle := (sp.start + sp.end) / 2

		if trimStart > 0 && middle < trimStart {
			marked[i], t.start = true, max(t.start, sp.end)
		}

		if trimEnd > 0 && middle > total-trimEnd {
			marked[i], t.end = true, max(t.end, total-sp.start)
		}
	}

	t.ranges = ranges(marked)

	return t
}

// timeline places the words: on the source for the first version, on the current reel for a revision.
func timeline(req *Request) (spans []span, total float64) {
	words := req.Transcript.Words
	spans = make([]span, len(words))

	if !req.IsRevision() {
		for i, w := range words {
			spans[i] = span{start: w.Start, end: w.End, ok: true}
		}

		return spans, req.Plan.SourceDuration
	}

	offset := 0.0

	for _, iv := range req.Plan.Keep {
		for i, w := range words {
			middle := (w.Start + w.End) / 2
			if middle < iv.Start || middle >= iv.End {
				continue
			}

			spans[i] = span{start: offset + max(w.Start, iv.Start) - iv.Start, end: offset + min(w.End, iv.End) - iv.Start, ok: true}
		}

		offset += iv.End - iv.Start
	}

	return spans, req.Plan.Duration
}

// timelineDuration is the length of the timeline trims count on.
func timelineDuration(req *Request) float64 {
	if req.IsRevision() {
		return req.Plan.Duration
	}

	return req.Plan.SourceDuration
}

// ranges turns marked words into ranges of neighbouring indices.
func ranges(marked []bool) []roughcut.WordRange {
	var out []roughcut.WordRange

	from := -1

	for i, m := range marked {
		switch {
		case m && from < 0:
			from = i
		case !m && from >= 0:
			out = append(out, roughcut.WordRange{From: from, To: i - 1})
			from = -1
		}
	}

	if from >= 0 {
		out = append(out, roughcut.WordRange{From: from, To: len(marked) - 1})
	}

	return out
}

// mergeEdit applies this decision to the previous edit: the words cut (the trim included) and brought back, the
// accepted candidates, the corrected words. The same word cut and brought back by one decision is refused.
func mergeEdit(req *Request, in *Input, trimmed []roughcut.WordRange) (roughcut.Edit, error) {
	previous := previousEdit(req.Plan)
	words := len(req.Transcript.Words)
	state := previousState(&previous, words)
	changed := make([]wordState, words)

	for _, r := range slices.Concat(trimmed, in.Remove, in.CleanupWords) {
		mark(changed, r, removedWord)
	}

	for _, r := range in.Restore {
		for i := r.From; i <= r.To; i++ {
			if changed[i] == removedWord {
				return roughcut.Edit{}, checkFailedf("слово %d и вырезано, и возвращено одной правкой", i)
			}
		}

		mark(changed, r, restoredWord)
	}

	for i, s := range changed {
		if s != unchangedWord {
			state[i] = s
		}
	}

	accept := slices.Clone(previous.Accept)

	for _, d := range in.Candidates {
		if d.Cut {
			accept = append(accept, d.ID)
		}
	}

	slices.Sort(accept)

	return roughcut.Edit{
		Accept:  slices.Compact(accept),
		Remove:  stateRanges(state, removedWord),
		Restore: stateRanges(state, restoredWord),
		Words:   mergeFixes(previous.Words, in.FixWords),
	}, nil
}

func previousEdit(plan *roughcut.Plan) roughcut.Edit {
	if plan.Edit == nil {
		return roughcut.Edit{}
	}

	return *plan.Edit
}

// previousState marks the words of the previous edit; ranges outside the transcript are dropped.
func previousState(previous *roughcut.Edit, words int) []wordState {
	state := make([]wordState, words)

	for _, r := range previous.Restore {
		mark(state, r, restoredWord)
	}

	for _, r := range previous.Remove {
		mark(state, r, removedWord)
	}

	return state
}

func mark[T any](state []T, r roughcut.WordRange, s T) {
	for i := max(r.From, 0); i <= r.To && i < len(state); i++ {
		state[i] = s
	}
}

func stateRanges(state []wordState, s wordState) []roughcut.WordRange {
	marked := make([]bool, len(state))
	for i := range state {
		marked[i] = state[i] == s
	}

	return ranges(marked)
}

// mergeFixes: a new correction of a word replaces the previous one; ordered by word, nil without corrections.
func mergeFixes(previous []roughcut.WordFix, fixes []FixInput) []roughcut.WordFix {
	if len(previous)+len(fixes) == 0 {
		return nil
	}

	byIndex := make(map[int]string, len(previous)+len(fixes))

	for _, f := range previous {
		byIndex[f.Index] = f.Text
	}

	for _, f := range fixes {
		byIndex[f.Index] = strings.TrimSpace(f.Text)
	}

	out := make([]roughcut.WordFix, 0, len(byIndex))
	for index, text := range byIndex {
		out = append(out, roughcut.WordFix{Index: index, Text: text})
	}

	slices.SortFunc(out, func(a, b roughcut.WordFix) int { return a.Index - b.Index })

	return out
}

// buildPlan builds the plan of the edit as the worker will (before the frame grid) and checks that a reel is left.
func buildPlan(req *Request, edit *roughcut.Edit) (roughcut.Plan, error) {
	plan, err := roughcut.BuildPlanEdited(req.Transcript, req.Plan.SourceDuration, req.Plan.Settings, edit)

	switch {
	case errors.Is(err, roughcut.ErrInvalidPlan):
		return roughcut.Plan{}, checkFailedf("план не строится: %v", err)
	case err != nil:
		return roughcut.Plan{}, fmt.Errorf("failed to build the edited plan: %w", err)
	case len(plan.Keep) == 0 || plan.Duration < minReelSeconds:
		return roughcut.Plan{}, checkFailedf("после правки от ролика остаётся %.1f с: так вырезать нельзя", plan.Duration)
	case len(plan.Keep) > roughcut.MaxSegments:
		return roughcut.Plan{}, checkFailedf("после правки в ролике %d кусков, а монтаж принимает не больше %d: "+
			"вырежи меньше отдельных отрезков или верни часть вырезанных", len(plan.Keep), roughcut.MaxSegments)
	}

	return plan, nil
}

// errNothingChanged: the revision changes nothing in the reel; the model did not find what to change.
var errNothingChanged = &CheckError{
	Message: "правка ничего не меняет в ролике: проверь индексы слов, или ответь decision unclear",
}

// checkChanged refuses a revision that changes nothing in the reel: the model did not find what to change.
func checkChanged(req *Request, edit *roughcut.Edit, plan *roughcut.Plan) error {
	previous := previousEdit(req.Plan)

	before, err := roughcut.BuildPlanEdited(req.Transcript, req.Plan.SourceDuration, req.Plan.Settings, &previous)
	if err != nil {
		return fmt.Errorf("failed to build the previous plan: %w", err)
	}

	if slices.Equal(before.Keep, plan.Keep) && slices.Equal(previous.Words, edit.Words) &&
		slices.Equal(previous.Titles, edit.Titles) && previous.Mute == edit.Mute && slices.Equal(previous.Hide, edit.Hide) &&
		previous.Hook == edit.Hook && slices.Equal(previous.ScreenNotes, edit.ScreenNotes) {
		return errNothingChanged
	}

	return nil
}

func countCandidates(decisions []CandidateDecision) (cut, kept int) {
	for _, d := range decisions {
		if d.Cut {
			cut++
		} else {
			kept++
		}
	}

	return cut, kept
}

// notes are the decisions in the decisions log: the trainer's request or the model's reason (always, so the log of a
// first version without a caption is not «Решения монтажёра: 0»), and every disputed repeat decided.
func notes(req *Request, in *Input, reason string, sel *selection) []string {
	out := make([]string, 0, len(in.Candidates)+1)

	switch {
	case req.IsRevision():
		out = append(out, fmt.Sprintf("Правка тренера к версии %d: %s", req.Version-1, orDash(reason)))
	case strings.TrimSpace(req.Caption) != "":
		out = append(out, "Подпись тренера к исходнику: "+orDash(reason))
	case sel.on:
		out = append(out, "Отбор под рилс: "+orDash(reason))
	case sel.cleanup:
		out = append(out, "Чистка первой версии: "+orDash(reason))
	default:
		out = append(out, "Решение по черновой склейке: "+orDash(reason))
	}

	for _, d := range in.Candidates {
		verdict := "оставлены обе фразы"
		if d.Cut {
			verdict = "первая фраза вырезана, оставлен повтор"
		}

		out = append(out, fmt.Sprintf("Повтор %d: %s — %s", d.ID, verdict, orDash(oneLine(d.Reason))))
	}

	return out
}

// details explain the decision in the decisions log without being decisions: the length of a shortened reel, every
// phrase left out of it, and the disputed repeats of a first version the model left undecided while both phrases
// stay in the reel.
func details(req *Request, in *Input, sel *selection) []string {
	out := slices.Concat(selectionNotes(req, in, sel), cleanupNotes(req, in, sel))

	if req.IsRevision() {
		return out
	}

	decided := make(map[int]bool, len(in.Candidates))
	for _, d := range in.Candidates {
		decided[d.ID] = true
	}

	kept := keptSet(req.Transcript.Words, sel.keep)

	for i := range req.Plan.Candidates {
		c := &req.Plan.Candidates[i]
		if !decided[c.ID] && anyKept(kept, c.EarlierWords) && anyKept(kept, c.LaterWords) {
			out = append(out, fmt.Sprintf("Повтор %d: решения нет — оставлены обе фразы", c.ID))
		}
	}

	return out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}

	return s
}

// oneLine drops control and invisible characters, collapses whitespace and line breaks and cuts the text to
// maxReasonRunes.
func oneLine(text string) string {
	visible := strings.Map(func(r rune) rune {
		if invisibleRune(r) {
			return ' '
		}

		return r
	}, text)

	return truncate(strings.Join(strings.Fields(visible), " "), maxReasonRunes)
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	return strings.TrimSpace(string(runes[:limit])) + "…"
}
