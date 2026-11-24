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
	"github.com/Prokud1n/ai-agents/internal/transcription"
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
	FixWords   []roughcut.WordFix   `json:"fix_words"`
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
func Apply(req *Request, in *Input) (*Outcome, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}

	reason := oneLine(in.Reason, maxReasonRunes)

	switch in.Decision {
	case CannotDecision:
		return &Outcome{Kind: CannotOutcome, Reason: reason}, nil
	case UnclearDecision:
		return &Outcome{Kind: UnclearOutcome, Reason: reason}, nil
	case EditDecision:
	default:
		return nil, checkFailedf("decision должен быть edit, cannot или unclear, а не %q", in.Decision)
	}

	if err := checkInput(req, in); err != nil {
		return nil, err
	}

	trimmed := trimRanges(req.Plan, req.Transcript.Words, in.TrimStart, in.TrimEnd)

	edit, err := mergeEdit(req, in, trimmed)
	if err != nil {
		return nil, err
	}

	edit.Notes = notes(req, in, reason)

	plan, err := buildPlan(req, &edit)
	if err != nil {
		return nil, err
	}

	if req.IsRevision() {
		if err := checkChanged(req, &edit, &plan); err != nil {
			return nil, err
		}
	}

	cut, kept := countCandidates(in.Candidates)

	return &Outcome{
		Kind: EditOutcome, Edit: edit, Changes: describeChanges(req, in, trimmed), Cut: cut, Kept: kept,
		Duration: plan.Duration, Reason: reason,
	}, nil
}

// checkInput checks the limits and that every index and ID exists.
func checkInput(req *Request, in *Input) error {
	words := len(req.Transcript.Words)

	switch {
	case len(in.Remove)+len(in.Restore) > maxRanges:
		return checkFailedf("слишком много отрезков: не больше %d в remove_words и restore_words вместе", maxRanges)
	case len(in.FixWords) > maxFixes:
		return checkFailedf("слишком много исправлений: не больше %d в fix_words", maxFixes)
	case !validSeconds(in.TrimStart) || !validSeconds(in.TrimEnd):
		return checkFailedf("trim_start_seconds и trim_end_seconds — неотрицательные числа секунд")
	case in.TrimStart+in.TrimEnd >= req.Plan.Duration:
		return checkFailedf("обрезка %.1f + %.1f с не меньше всего ролика (%.1f с)", in.TrimStart, in.TrimEnd,
			req.Plan.Duration)
	}

	for _, ranges := range [][]roughcut.WordRange{in.Remove, in.Restore} {
		for _, r := range ranges {
			if r.From < 0 || r.To < r.From || r.To >= words {
				return checkFailedf("отрезок слов %d–%d вне расшифровки: индексы от 0 до %d, from не больше to",
					r.From, r.To, words-1)
			}
		}
	}

	if err := checkCandidates(req.Plan, in.Candidates); err != nil {
		return err
	}

	return checkFixes(words, in.FixWords)
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

func checkFixes(words int, fixes []roughcut.WordFix) error {
	seen := make(map[int]bool, len(fixes))

	for _, f := range fixes {
		text := strings.TrimSpace(f.Text)

		switch {
		case f.Index < 0 || f.Index >= words:
			return checkFailedf("слова с индексом %d нет в расшифровке", f.Index)
		case seen[f.Index]:
			return checkFailedf("слово %d исправлено дважды", f.Index)
		case text == "" || utf8.RuneCountInString(text) > maxWordRunes:
			return checkFailedf("исправление слова %d: от 1 до %d символов", f.Index, maxWordRunes)
		case strings.ContainsFunc(text, forbiddenRune):
			return checkFailedf("исправление слова %d: без переносов строк и символов { } \\ < >", f.Index)
		}

		seen[f.Index] = true
	}

	return nil
}

// forbiddenRune: control characters and line breaks, and what subtitles or tags could read as markup.
func forbiddenRune(r rune) bool {
	return unicode.IsControl(r) || strings.ContainsRune(`{}\<>`, r)
}

// trimRanges are the words of the reel that start within its first trimStart seconds or end within its last trimEnd
// seconds, on the timeline of the current plan.
func trimRanges(plan *roughcut.Plan, words []transcription.Word, trimStart, trimEnd float64) []roughcut.WordRange {
	if trimStart <= 0 && trimEnd <= 0 {
		return nil
	}

	marked := make([]bool, len(words))
	offset := 0.0

	for _, iv := range plan.Keep {
		for i, w := range words {
			middle := (w.Start + w.End) / 2
			if middle < iv.Start || middle >= iv.End {
				continue
			}

			start := offset + max(w.Start, iv.Start) - iv.Start
			end := offset + min(w.End, iv.End) - iv.Start
			marked[i] = (trimStart > 0 && start < trimStart) || (trimEnd > 0 && end > plan.Duration-trimEnd)
		}

		offset += iv.End - iv.Start
	}

	return ranges(marked)
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

	for _, r := range slices.Concat(trimmed, in.Remove) {
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

func mark(state []wordState, r roughcut.WordRange, s wordState) {
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
func mergeFixes(previous, fixes []roughcut.WordFix) []roughcut.WordFix {
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
	}

	return plan, nil
}

// checkChanged refuses a revision that changes nothing in the reel: the model did not find what to change.
func checkChanged(req *Request, edit *roughcut.Edit, plan *roughcut.Plan) error {
	previous := previousEdit(req.Plan)

	before, err := roughcut.BuildPlanEdited(req.Transcript, req.Plan.SourceDuration, req.Plan.Settings, &previous)
	if err != nil {
		return fmt.Errorf("failed to build the previous plan: %w", err)
	}

	if slices.Equal(before.Keep, plan.Keep) && slices.Equal(previous.Words, edit.Words) {
		return checkFailedf("правка ничего не меняет в ролике: проверь индексы слов, или ответь decision unclear")
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

// notes explain the decision in the decisions log: the trainer's request and every disputed repeat.
func notes(req *Request, in *Input, reason string) []string {
	out := make([]string, 0, len(in.Candidates)+1)

	switch {
	case req.IsRevision():
		out = append(out, fmt.Sprintf("Правка тренера к версии %d: %s", req.Version-1, orDash(reason)))
	case strings.TrimSpace(req.Caption) != "":
		out = append(out, "Подпись тренера к исходнику: "+orDash(reason))
	}

	for _, d := range in.Candidates {
		verdict := "оставлены обе фразы"
		if d.Cut {
			verdict = "первая фраза вырезана, оставлен повтор"
		}

		out = append(out, fmt.Sprintf("Повтор %d: %s — %s", d.ID, verdict, orDash(oneLine(d.Reason, maxReasonRunes))))
	}

	return out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}

	return s
}

// oneLine collapses whitespace and line breaks and cuts the text to limit runes.
func oneLine(text string, limit int) string {
	return truncate(strings.Join(strings.Fields(text), " "), limit)
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	return strings.TrimSpace(string(runes[:limit])) + "…"
}
