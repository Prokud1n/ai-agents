package subtitles

import (
	"cmp"
	"fmt"
	"math"
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
	// readingWindow: a phrase that starts this close to a heading and ends before its min_duration reads the heading
	// out («страх номер один»): the heading stays for the phrase after it. The words that read it out start this
	// close to it too (hideReadings).
	readingWindow = 0.3
)

// SectionStyle is the look and the place of the section headings of a talking head («СТРАХ 1», «СТРАХ 2»): one line
// centered above the subtitles, shown from the start of a section until the end of its first phrase (the question or
// the thesis; the phrase that reads the heading out is skipped, and its words are not shown in the subtitles),
// MinDuration..MaxDuration seconds; the next heading replaces it. Disabled: the headings are drawn as callouts of the
// screen notes, in the hook's style at its place.
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
		MaxDuration: 6,
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

// sectionBlocks lays the headings out in time: each waits for the hook to go, is shown until the end of the first
// phrase of its section (its end when set) within min_duration..max_duration, and ends at the next heading and at the
// end of the reel (reelEnd 0: unknown). A heading left shorter than minNoteShown is not drawn.
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

// sectionShown is how long a heading shown from start stays: its own length when it has an end, otherwise until the
// end of the first phrase of its section (sectionPhraseEnd); within min_duration..max_duration.
func sectionShown(n Note, start float64, result *transcription.Result, c *SectionStyle) float64 {
	shown := c.MinDuration

	if n.End > n.Start {
		shown = n.End - n.Start
	} else if end, ok := sectionPhraseEnd(result, n.Start, start, c); ok {
		// The subtitle of the phrase stays hold after it: the heading goes with it.
		shown = end + hold - start
	}

	return min(max(shown, c.MinDuration), c.MaxDuration)
}

// readsHeading: a phrase from start to end reads out the heading that starts at heading (it starts within
// readingWindow of it and ends before its min_duration is over).
func readsHeading(start, end, heading float64, c *SectionStyle) bool {
	return math.Abs(start-heading) <= readingWindow && end < heading+c.MinDuration
}

// sectionPhraseEnd is the end of the first phrase of the section whose heading starts at heading and is shown from
// shown: the first segment that ends after shown and does not read the heading out; without segments the first phrase
// that ends min_duration after shown (phraseEndAfter). false when the speech ends before.
func sectionPhraseEnd(result *transcription.Result, heading, shown float64, c *SectionStyle) (float64, bool) {
	if len(result.Segments) == 0 {
		return phraseEndAfter(result, shown+c.MinDuration)
	}

	for _, seg := range result.Segments {
		if strings.TrimSpace(seg.Text) == "" || seg.End <= shown || readsHeading(seg.Start, seg.End, heading, c) {
			continue
		}

		return seg.End, true
	}

	return 0, false
}

// phraseEndAfter is the end of the first phrase that ends at at or later: a word followed by a pause (or the last
// word); false when the speech ends before.
func phraseEndAfter(result *transcription.Result, at float64) (float64, bool) {
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
// a segment) small, as the trainer writes her subtitles (layout.lowercase_sentences); a word with more capitals (МФЦ,
// PDF, YouTube) stays as it is. A name at the start of a sentence becomes small too: Whisper does not tell it apart.
// tokens is not changed.
func lowerSentenceStarts(tokens []token) []token {
	out := make([]token, 0, len(tokens))

	for i, t := range tokens {
		if (i == 0 || tokens[i-1].Break == strongBreak) && capitals(t.Text) < 2 {
			t.Text = lowerFirstLetter(t.Text)
		}

		out = append(out, t)
	}

	return out
}

// capitals counts the capital letters of the text.
func capitals(text string) int {
	n := 0

	for _, r := range text {
		if unicode.IsUpper(r) {
			n++
		}
	}

	return n
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

// hideUnderHook keeps the subtitles off the screen while the hook is shown (hook.hide_subtitles): a subtitle that ends by
// hookEnd is not shown, one that starts before it is shown from it (when minNoteShown of it is left). cues is not
// changed.
func hideUnderHook(cues []cue, hookEnd float64) []cue {
	if hookEnd <= 0 {
		return cues
	}

	out := make([]cue, 0, len(cues))

	for _, c := range cues {
		if c.Start < hookEnd {
			c.Start = hookEnd
		}

		if c.End-c.Start >= minNoteShown {
			out = append(out, c)
		}
	}

	return out
}

// separateBlocks keeps the section headings and the other screen notes apart in time (they stand at nearly the same
// place): a note that starts while a heading is shown replaces it, a note shown when a heading starts ends there. A
// block left shorter than minNoteShown is not drawn.
func separateBlocks(notes, sections []noteBlock) (keptNotes, keptSections []noteBlock) {
	if len(notes) == 0 || len(sections) == 0 {
		return notes, sections
	}

	keptSections = make([]noteBlock, 0, len(sections))

	for _, s := range sections {
		for _, n := range notes {
			if n.start >= s.start && n.start < s.end {
				s.end = n.start
			}
		}

		if s.end-s.start >= minNoteShown {
			keptSections = append(keptSections, s)
		}
	}

	keptNotes = make([]noteBlock, 0, len(notes))

	for _, n := range notes {
		for _, s := range keptSections {
			if s.start > n.start && s.start < n.end {
				n.end = s.start
			}
		}

		if n.end-n.start >= minNoteShown {
			keptNotes = append(keptNotes, n)
		}
	}

	return withFades(keptNotes), keptSections
}
