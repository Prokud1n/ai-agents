package editplan

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

const (
	// maxTitles of a reel (a workout has a handful of exercises, a long one a dozen or two).
	maxTitles = 20
	// maxTitleRunes of an exercise title: «Болгарские сплит-приседания — 12 раз» fits, a sentence does not; libass
	// wraps it into at most two lines.
	maxTitleRunes = 40
	// minTitleGap between the starts of two titles: one would hide the other at once.
	minTitleGap = 0.5
	// millisPerSecond: times of the source are kept to milliseconds.
	millisPerSecond = 1000
)

// SecondsRange is a stretch of the timeline the decision counts on (the source for the caption, the reel for a
// revision), in seconds.
type SecondsRange struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

// TitleInput is a new exercise title: it starts at the word Word (where the trainer names the exercise) or at Start
// seconds of the timeline, and ends at End seconds or, without one, at the next title (the last one at the end of the
// reel).
type TitleInput struct {
	Text  string   `json:"text"`
	Word  *int     `json:"word,omitempty"`
	Start *float64 `json:"start_seconds,omitempty"`
	End   *float64 `json:"end_seconds,omitempty"`
}

// TitleChange changes the title Index of the list in the prompt: what is set replaces it.
type TitleChange struct {
	Index int      `json:"index"`
	Text  string   `json:"text,omitempty"`
	Word  *int     `json:"word,omitempty"`
	Start *float64 `json:"start_seconds,omitempty"`
	End   *float64 `json:"end_seconds,omitempty"`
}

// nextMode is the mode of the new version: the decision's, or the previous version's.
func nextMode(req *Request, in *Input) roughcut.Mode {
	if in.Mode != "" {
		return in.Mode.Effective()
	}

	previous := previousEdit(req.Plan)

	return previous.Mode.Effective()
}

// storedMode is the mode as the edit keeps it: empty for a talking head, so its plans stay as they were.
func storedMode(m roughcut.Mode) roughcut.Mode {
	if m == roughcut.WorkoutMode {
		return roughcut.WorkoutMode
	}

	return ""
}

// checkWorkoutInput checks the mode, the cuts by time, the titles and their limits.
func checkWorkoutInput(req *Request, in *Input) error {
	switch in.Mode {
	case "", roughcut.TalkMode, roughcut.WorkoutMode:
	default:
		return checkFailedf("mode должен быть talk или workout, а не %q", in.Mode)
	}

	total := timelineDuration(req)

	if len(in.CutSeconds) > maxRanges {
		return checkFailedf("слишком много отрезков: не больше %d в cut_seconds", maxRanges)
	}

	for _, r := range in.CutSeconds {
		if !validSeconds(r.From) || !validSeconds(r.To) || r.To <= r.From || r.From >= total {
			return checkFailedf("отрезок %.1f–%.1f с вне видео: from < to, от 0 до %.1f с", r.From, r.To, total)
		}
	}

	previous := previousEdit(req.Plan)

	if err := checkIndices("restore_cuts", in.RestoreCuts, len(previous.CutTime)); err != nil {
		return err
	}

	if len(in.AddTitles)+len(in.ChangeTitles) > maxTitles || len(in.UntimedTitles) > maxTitles {
		return checkFailedf("слишком много подписей упражнений: не больше %d", maxTitles)
	}

	for _, text := range in.UntimedTitles {
		if err := checkTitleText(text); err != nil {
			return err
		}
	}

	return checkTitleIndices(in, len(previous.Titles))
}

// checkIndices: every index is in the list and named once.
func checkIndices(field string, indices []int, size int) error {
	seen := make(map[int]bool, len(indices))

	for _, i := range indices {
		if i < 0 || i >= size || seen[i] {
			return checkFailedf("%s: номера от 0 до %d, каждый один раз (а не %d)", field, size-1, i)
		}

		seen[i] = true
	}

	return nil
}

// checkTitleIndices: the changed and removed titles exist, and a title is not both changed and removed.
func checkTitleIndices(in *Input, size int) error {
	changed := make([]int, 0, len(in.ChangeTitles))
	for _, c := range in.ChangeTitles {
		changed = append(changed, c.Index)
	}

	if err := checkIndices("change_titles", changed, size); err != nil {
		return err
	}

	if err := checkIndices("remove_titles", in.RemoveTitles, size); err != nil {
		return err
	}

	for _, i := range in.RemoveTitles {
		if slices.Contains(changed, i) {
			return checkFailedf("подпись %d и изменена, и убрана одной правкой", i)
		}
	}

	return nil
}

// checkTitleText: one short line of visible text without markup.
func checkTitleText(text string) error {
	trimmed := strings.TrimSpace(text)

	switch {
	case trimmed == "" || utf8.RuneCountInString(trimmed) > maxTitleRunes:
		return checkFailedf("подпись упражнения «%s»: от 1 до %d символов", truncate(trimmed, maxTitleRunes), maxTitleRunes)
	case strings.ContainsFunc(trimmed, forbiddenRune):
		return checkFailedf("подпись упражнения «%s»: без переносов строк, невидимых символов и { } \\ < >",
			truncate(oneLine(trimmed), maxTitleRunes))
	}

	return nil
}

// trimTime is a trim of a workout: by time, not by words, so the exercise before the first word is cut as asked.
func trimTime(req *Request, trimStart, trimEnd float64) trim {
	var t trim

	total := timelineDuration(req)

	if trimStart > 0 {
		t.start = trimStart
		t.cuts = append(t.cuts, sourceSpan(req, 0, trimStart)...)
	}

	if trimEnd > 0 {
		t.end = trimEnd
		t.cuts = append(t.cuts, sourceSpan(req, total-trimEnd, total)...)
	}

	return t
}

// sourceSpan is the stretch [from, to) of the timeline as fragments of the source: the same for the caption, mapped
// through the kept intervals of the reel for a revision.
func sourceSpan(req *Request, from, to float64) []roughcut.Interval {
	if !req.IsRevision() {
		return []roughcut.Interval{{Start: from, End: to}}
	}

	return roughcut.SourceIntervals(req.Plan.Keep, from, to)
}

// sourceTime is a moment of the timeline as the time of the source.
func sourceTime(req *Request, t float64) float64 {
	if !req.IsRevision() {
		return t
	}

	return roughcut.SourceTime(req.Plan.Keep, t)
}

// newTitles are the titles this decision added (in the order of add_titles) and changed (by their number in the
// previous version), as the edit keeps them: for the description of the changes.
type newTitles struct {
	added   []roughcut.Title
	changed map[int]roughcut.Title
}

// mergeExtras puts the mode, the cuts by time, the titles, the sound, the hook title and the screen notes of this
// decision onto the edit of the new version.
func mergeExtras(req *Request, in *Input, mode roughcut.Mode, trimmed *trim, edit *roughcut.Edit) (*newTitles, error) {
	previous := previousEdit(req.Plan)

	cuts := make([]roughcut.Interval, 0, len(previous.CutTime)+len(trimmed.cuts)+len(in.CutSeconds))

	for i, iv := range previous.CutTime {
		if !slices.Contains(in.RestoreCuts, i) {
			cuts = append(cuts, iv)
		}
	}

	cuts = append(cuts, trimmed.cuts...)

	for _, r := range in.CutSeconds {
		cuts = append(cuts, sourceSpan(req, r.From, r.To)...)
	}

	titles, news, err := mergeTitles(req, in, previous.Titles)
	if err != nil {
		return nil, err
	}

	hide, err := mergeHidden(req, in, previous.Hide)
	if err != nil {
		return nil, err
	}

	notes, err := screenNotesFor(req, in, mode, &previous)
	if err != nil {
		return nil, err
	}

	edit.Mode = storedMode(mode)
	edit.Hide = hide
	edit.CutTime = roughcut.NormalizeIntervals(cuts, req.Plan.SourceDuration)
	edit.Titles = titles
	edit.Mute = previous.Mute
	edit.Hook = nextHook(&previous, in)
	edit.ScreenNotes = notes

	if in.Mute != nil {
		edit.Mute = *in.Mute
	}

	return news, nil
}

// mergeTitles applies the changes, removals and additions to the titles of the previous version; the result is
// ordered by time, nil without titles.
func mergeTitles(req *Request, in *Input, previous []roughcut.Title) ([]roughcut.Title, *newTitles, error) {
	out := make([]roughcut.Title, 0, len(previous)+len(in.AddTitles))
	news := &newTitles{changed: make(map[int]roughcut.Title, len(in.ChangeTitles))}

	for i, t := range previous {
		if slices.Contains(in.RemoveTitles, i) {
			continue
		}

		for _, c := range in.ChangeTitles {
			if c.Index != i {
				continue
			}

			changed, err := changeTitle(req, t, &c)
			if err != nil {
				return nil, nil, err
			}

			t = changed
			news.changed[i] = changed
		}

		out = append(out, t)
	}

	for i := range in.AddTitles {
		t, err := changeTitle(req, roughcut.Title{}, &TitleChange{
			Index: -1, Text: in.AddTitles[i].Text, Word: in.AddTitles[i].Word, Start: in.AddTitles[i].Start,
			End: in.AddTitles[i].End,
		})
		if err != nil {
			return nil, nil, err
		}

		out = append(out, t)
		news.added = append(news.added, t)
	}

	titles, err := orderTitles(out)
	if err != nil {
		return nil, nil, err
	}

	return titles, news, nil
}

// changeTitle returns the title with the set fields of the change; a new title (Index -1) needs a text and a start.
func changeTitle(req *Request, t roughcut.Title, c *TitleChange) (roughcut.Title, error) {
	isNew := c.Index < 0

	switch {
	case c.Word != nil && c.Start != nil:
		return t, checkFailedf("подпись «%s»: укажи начало или словом (word), или секундами (start_seconds)",
			titleName(t, c))
	case isNew && c.Word == nil && c.Start == nil:
		return t, checkFailedf("подпись «%s»: нет начала — укажи word или start_seconds; если время неизвестно, "+
			"перенеси её в untimed_titles", c.Text)
	case isNew || c.Text != "":
		if err := checkTitleText(c.Text); err != nil {
			return t, err
		}

		t.Text = strings.TrimSpace(c.Text)
	}

	return retimeTitle(req, t, c)
}

// titleName names the title in a check failure: the new text, or the text it has.
func titleName(t roughcut.Title, c *TitleChange) string {
	if text := strings.TrimSpace(c.Text); text != "" {
		return text
	}

	return t.Text
}

// retimeTitle sets the start and the end of the change on the title, on the source.
func retimeTitle(req *Request, t roughcut.Title, c *TitleChange) (roughcut.Title, error) {
	start, err := titleStart(req, c, t.Text)
	if err != nil {
		return t, err
	}

	if start >= 0 {
		t.Start = start
	}

	if c.End != nil {
		total := timelineDuration(req)
		if !validSeconds(*c.End) || *c.End > total {
			return t, checkFailedf("подпись «%s»: end_seconds от 0 до %.1f с", t.Text, total)
		}

		t.End = roundSeconds(sourceTime(req, *c.End))
	}

	if t.End != 0 && t.End <= t.Start {
		return t, checkFailedf("подпись «%s»: конец раньше начала", t.Text)
	}

	return t, nil
}

// titleStart is the start of the change on the source, or -1 when the change keeps it; name is the title's text for
// a check failure.
func titleStart(req *Request, c *TitleChange, name string) (float64, error) {
	switch {
	case c.Word != nil:
		if *c.Word < 0 || *c.Word >= len(req.Transcript.Words) {
			return 0, checkFailedf("подпись «%s»: слова с индексом %d нет в расшифровке", name, *c.Word)
		}

		return roundSeconds(req.Transcript.Words[*c.Word].Start), nil
	case c.Start != nil:
		total := timelineDuration(req)
		if !validSeconds(*c.Start) || *c.Start >= total {
			return 0, checkFailedf("подпись «%s»: start_seconds от 0 до %.1f с", name, total)
		}

		return roundSeconds(sourceTime(req, *c.Start)), nil
	default:
		return -1, nil
	}
}

// orderTitles sorts the titles by time and refuses two that start together.
func orderTitles(titles []roughcut.Title) ([]roughcut.Title, error) {
	if len(titles) == 0 {
		return nil, nil
	}

	if len(titles) > maxTitles {
		return nil, checkFailedf("слишком много подписей упражнений: не больше %d", maxTitles)
	}

	slices.SortStableFunc(titles, func(a, b roughcut.Title) int { return cmp.Compare(a.Start, b.Start) })

	for i := 1; i < len(titles); i++ {
		if titles[i].Start-titles[i-1].Start < minTitleGap {
			return nil, checkFailedf("подписи «%s» и «%s» начинаются почти одновременно: одна закрыла бы другую",
				titles[i-1].Text, titles[i].Text)
		}
	}

	return titles, nil
}

// checkTitlesShown: every title is seen in the reel built with the edit; it returns them on the reel's timeline.
func checkTitlesShown(edit *roughcut.Edit, plan *roughcut.Plan) ([]roughcut.Title, error) {
	shown := roughcut.RemapTitles(edit.Titles, plan.Keep)
	if hidden := len(edit.Titles) - len(shown); hidden > 0 {
		return nil, checkFailedf("подписей упражнений не видно в ролике: %d (их время вырезано или они короче "+
			"%.1f с) — поставь их на время, которое осталось в ролике", hidden, roughcut.MinTitleSeconds)
	}

	return shown, nil
}

// titleLines are the titles for the description of the draft: «0:15 — «Выпады — 12 раз»».
func titleLines(shown []roughcut.Title) []string {
	lines := make([]string, 0, len(shown))
	for _, t := range shown {
		lines = append(lines, fmt.Sprintf("%s — «%s»", clock(t.Start), t.Text))
	}

	return lines
}

// untimedTitles are the exercises the editor could not place in time, cleaned for the description.
func untimedTitles(in *Input) []string {
	out := make([]string, 0, len(in.UntimedTitles))
	for _, text := range in.UntimedTitles {
		out = append(out, strings.TrimSpace(text))
	}

	return out
}

// roundSeconds keeps milliseconds, as the plans do.
func roundSeconds(t float64) float64 {
	return math.Round(t*millisPerSecond) / millisPerSecond
}

// mergeHidden adds the words this decision hides in the subtitles to those of the previous versions; a word hidden
// already is refused, like a correction that changes nothing. Nil without hidden words.
func mergeHidden(req *Request, in *Input, previous []roughcut.WordRange) ([]roughcut.WordRange, error) {
	hidden := make([]bool, len(req.Transcript.Words))
	for _, r := range previous {
		mark(hidden, r, true)
	}

	for _, r := range in.Hide {
		for i := r.From; i <= r.To; i++ {
			if hidden[i] {
				return nil, checkFailedf("слово %d уже скрыто в субтитрах", i)
			}
		}

		mark(hidden, r, true)
	}

	return ranges(hidden), nil
}
