package subtitles

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	sectionStyleName = "Section"
	// minSectionDuration..maxSectionDuration: the limits of section.min_duration and section.max_duration in a style
	// file.
	minSectionDuration = 1.0
	maxSectionDuration = 10.0
)

// SectionStyle is the look and the place of the section headings of a talking head («СТРАХ 1», «СТРАХ 2»): one line
// centered above the subtitles, shown from the start of a section while its first phrase (the question or the thesis)
// is said, MinDuration..MaxDuration seconds; the next heading replaces it. Disabled: the headings are drawn as
// callouts of the screen notes, in the hook's style at its place.
type SectionStyle struct {
	TextStyle `yaml:",inline"`

	Enabled bool `yaml:"enabled"`
	// Margin is the top of the line from the top of the frame (the line is as tall as the font size).
	Margin      int     `yaml:"margin"`
	MinDuration float64 `yaml:"min_duration"`
	MaxDuration float64 `yaml:"max_duration"`
	// Fade in and out, in milliseconds.
	Fade int `yaml:"fade"`
}

// defaultSection is the look of the trainer's headings: peach Fira Sans Extra Condensed ExtraBold capitals, the
// 47 px capitals of her «СТРАХ 1» centered at 410 px. Off outside the trainer's style.
func defaultSection() SectionStyle {
	return SectionStyle{
		TextStyle:   TextStyle{Font: defaultAccentFont, Size: 78, Uppercase: true, Color: peach},
		Margin:      372,
		MinDuration: 2.5,
		MaxDuration: 4,
		Fade:        150,
	}
}

// validateSection checks the look and the times of the headings and, when they are on, their place.
func validateSection(s *Style) []error {
	c := &s.Section
	errs := validateText("section", &c.TextStyle)

	if c.Margin < SafeZoneTop || c.Margin > maxVertMargin {
		errs = append(errs, fmt.Errorf("section.margin must be %d..%d", SafeZoneTop, maxVertMargin))
	}

	if c.MinDuration < minSectionDuration || c.MaxDuration > maxSectionDuration || c.MinDuration > c.MaxDuration {
		errs = append(errs, fmt.Errorf("section.min_duration and section.max_duration must be %.0f..%.0f s, the "+
			"minimum not above the maximum", minSectionDuration, maxSectionDuration))
	}

	if c.Fade < 0 || c.Fade > maxHookFade || float64(2*c.Fade) >= c.MinDuration*millisPerSecond {
		errs = append(errs, fmt.Errorf("section.fade must be 0..%d ms and shorter than half of section.min_duration",
			maxHookFade))
	}

	if len(errs) > 0 || !c.Enabled {
		return errs
	}

	return validateSectionPlace(s)
}

// validateSectionPlace: the heading stays between the Instagram zones, clear of the subtitles (or the words) and the
// exercise titles. It never stands with the hook: it waits for the hook to go.
func validateSectionPlace(s *Style) []error {
	var errs []error

	top, bottom := s.sectionSpan()
	if outsideZones(top, bottom) {
		errs = append(errs, fmt.Errorf("section headings (%d..%d px) must stay between the Instagram zones (%d..%d px)",
			top, bottom, SafeZoneTop, FrameHeight-SafeZoneBottom))
	}

	others := map[string]rect{titlesPlaceName: band(s.titleSpan())}

	if s.wordMode() {
		others[wordsPlaceName] = s.wordBoxRect()
	} else {
		others[subtitlesPlaceName] = band(s.subtitlesSpan())
	}

	for _, name := range sortedKeys(others) {
		if other := others[name]; band(top, bottom).overlaps(other) {
			errs = append(errs, fmt.Errorf("section headings (%d..%d px) overlap %s (%d..%d px): move section.margin",
				top, bottom, name, other.top, other.bottom))
		}
	}

	return errs
}

// sectionSpan is the line of the headings: libass makes a line as tall as the font size.
func (s *Style) sectionSpan() (top, bottom int) {
	return s.Section.Margin, s.Section.Margin + s.Section.Size
}

// sectionLineChars is how many characters of a heading fit between the hook's side margins unsqueezed.
func (s *Style) sectionLineChars() int {
	width := float64(FrameWidth - 2*s.hookMarginSide())

	return max(int(width/(float64(s.Section.Size)*s.Hook.CharWidth)), 1)
}

// splitSections separates the section headings from the other screen notes when the style draws them (section.enabled);
// otherwise they stay with the notes (drawn as callouts).
func splitSections(notes []Note, style *Style) (others, sections []Note) {
	if !style.Section.Enabled {
		return notes, nil
	}

	for _, n := range notes {
		if n.Kind == SectionNote {
			sections = append(sections, n)
		} else {
			others = append(others, n)
		}
	}

	return others, sections
}

// firstSection is the start of the first section heading with a text; false without one.
func firstSection(notes []Note, style *Style) (float64, bool) {
	first, found := 0.0, false

	for _, n := range notes {
		if n.Kind != SectionNote || len(cleanWords(n.Text, style.Section.Uppercase)) == 0 {
			continue
		}

		if !found || n.Start < first {
			first, found = n.Start, true
		}
	}

	return first, found
}

// sectionBlocks lays the headings out in time: each waits for the hook to go, is shown while the first phrase of its
// section is said (its end when set) within min_duration..max_duration, and ends at the next heading and at the end of
// the reel (reelEnd 0: unknown). A heading left shorter than minNoteShown is not drawn.
func sectionBlocks(sections []Note, hookEnd, reelEnd float64, result *transcription.Result, style *Style) []noteBlock {
	sorted := make([]Note, 0, len(sections))

	for _, n := range sections {
		if words := cleanWords(n.Text, style.Section.Uppercase); len(words) > 0 {
			n.Text = strings.Join(words, " ")
			sorted = append(sorted, n)
		}
	}

	slices.SortStableFunc(sorted, func(a, b Note) int { return cmp.Compare(a.Start, b.Start) })

	blocks := make([]noteBlock, 0, len(sorted))

	for i, n := range sorted {
		start := max(n.Start, hookEnd)
		end := start + sectionShown(n, start, result, &style.Section)

		if i+1 < len(sorted) {
			end = min(end, max(sorted[i+1].Start, hookEnd))
		}

		if reelEnd > 0 {
			end = min(end, reelEnd)
		}

		if end-start < minNoteShown {
			continue
		}

		blocks = append(blocks, noteBlock{
			start: roundCS(start), end: roundCS(end), entries: []string{n.Text}, lines: 1, fadeIn: true, fadeOut: true,
		})
	}

	return blocks
}

// sectionShown is how long a heading shown from start stays: its own length when it has an end, otherwise until
// phraseTail after the first phrase that ends at least min_duration after the start; within min_duration..max_duration.
func sectionShown(n Note, start float64, result *transcription.Result, c *SectionStyle) float64 {
	shown := c.MinDuration

	if n.End > n.Start {
		shown = n.End - n.Start
	} else if end, ok := phraseEndAfter(result, start+c.MinDuration-phraseTail); ok {
		shown = end + phraseTail - start
	}

	return min(max(shown, c.MinDuration), c.MaxDuration)
}

// phraseEndAfter is the end of the first phrase that ends at at or later: a segment of the transcript with words, or
// without segments a word followed by a pause (or the last word); false when the speech ends before.
func phraseEndAfter(result *transcription.Result, at float64) (float64, bool) {
	for _, seg := range result.Segments {
		if strings.TrimSpace(seg.Text) != "" && seg.End >= at {
			return seg.End, true
		}
	}

	words := result.Words
	for i, w := range words {
		if w.End >= at && (i+1 == len(words) || words[i+1].Start-w.End >= pauseBreak) {
			return w.End, true
		}
	}

	return 0, false
}

// sectionEvents are the dialogue lines of the headings: the Section style at its place, each fading in and out,
// squeezed to the width when longer. The text is plain: it is escaped like speech.
func sectionEvents(blocks []noteBlock, style *Style) string {
	var b strings.Builder

	for _, bl := range blocks {
		fade := min(style.Section.Fade, int((bl.end-bl.start)*millisPerSecond)/2)
		text := strings.Join(bl.entries, " ")
		tags := fadTag(fade, fade) + scaleTag(charsScale(utf8.RuneCountInString(text), style.sectionLineChars()))

		fmt.Fprintf(&b, "Dialogue: %d,%s,%s,%s,,0,0,0,,{%s}%s\n", hookLayer, formatTime(bl.start), formatTime(bl.end),
			sectionStyleName, tags, escapeText(text))
	}

	return b.String()
}

// sectionStyleLine is the style of the headings: centered at the top at section.margin within the hook's side margins,
// with the outline of the subtitles and the shadow of the hook.
func sectionStyleLine(style *Style) string {
	spec := styleSpec{
		name: sectionStyleName, text: &style.Section.TextStyle, outline: style.Outline.Color,
		back: style.Hook.Shadow.Color, borderStyle: outlineBorderStyle, outlineSize: style.Outline.Size,
		shadow: style.Hook.Shadow.Size, alignment: topAlignment, marginL: style.hookMarginSide(),
		marginR: style.hookMarginSide(), marginV: style.Section.Margin,
	}

	return spec.line()
}

// lowerSentenceStarts writes the first letter of every sentence (the first word, a word after the end of a sentence or
// a segment) small, as the trainer writes her subtitles (layout.lowercase_sentences). tokens is not changed.
func lowerSentenceStarts(tokens []token) []token {
	out := make([]token, 0, len(tokens))

	for i, t := range tokens {
		if i == 0 || tokens[i-1].Break == strongBreak {
			t.Text = lowerFirstLetter(t.Text)
		}

		out = append(out, t)
	}

	return out
}

// lowerFirstLetter makes the first letter of the text small.
func lowerFirstLetter(text string) string {
	for i, r := range text {
		if unicode.IsLetter(r) {
			return text[:i] + string(unicode.ToLower(r)) + text[i+utf8.RuneLen(r):]
		}
	}

	return text
}
