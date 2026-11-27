package subtitles

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	mainStyleName   = "Main"
	accentStyleName = "Accent"
	titleStyleName  = "Title"
	// titleLayer draws the exercise titles above the speech subtitles.
	titleLayer = 1
	// ASS BorderStyle: 1 is an outline with a drop shadow, 3 an opaque box (filled with the outline color).
	outlineBorderStyle = 1
	boxBorderStyle     = 3

	// ASS \an alignment: 8 is top center, 5 middle center, 2 bottom center.
	topAlignment    = 8
	middleAlignment = 5
	bottomAlignment = 2

	assBold      = -1
	assLineBreak = `\N`
	// minScaleX is the narrowest a line is squeezed (\fscx) when one word is longer than the line limit.
	minScaleX      = 50
	fullScaleX     = 100
	percentBase    = 100
	csPerMinute    = 60 * centisecondsPerSecond
	csPerHour      = 60 * csPerMinute
	assFloatFormat = 'f'
	float64Bits    = 64
)

const styleFormat = "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, " +
	"Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, " +
	"Alignment, MarginL, MarginR, MarginV, Encoding"

const eventFormat = "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text"

// buildASS makes the whole subtitle file for a transcript, the exercise titles and the hook title.
func buildASS(
	result *transcription.Result,
	style *Style,
	accents []string,
	karaoke bool,
	titles []Title,
	hook string,
) (ass string, cues int) {
	return buildDocument(&CreateSubtitlesData{
		Transcript: result, Accents: accents, Karaoke: karaoke, Titles: titles, Hook: hook,
	}, style)
}

// buildDocument makes the whole subtitle file: the speech (by words or by phrases), the exercise titles, the hook
// title, the screen notes and the section headings after it, the top line, the handle and the end card. cues counts
// the speech events.
func buildDocument(data *CreateSubtitlesData, style *Style) (ass string, cues int) {
	result := data.Transcript
	end := hookEnd(data.Hook, result, data.Notes, style)

	// Under the end card nothing of the speech or the notes is drawn: they end where it starts (limit; 0 without a
	// card).
	limit, _ := cardTimes(data.Branding, result.Duration, style)

	notesEnd := result.Duration
	if limit > 0 {
		notesEnd = limit
	}

	notes, sections := splitSections(data.Notes, style)
	blocks, headings := separateBlocks(noteBlocks(notes, end, notesEnd, style),
		sectionBlocks(sections, end, notesEnd, result, style))
	extras := titleEvents(data.Titles, style) + hookEvent(data.Hook, end, style) + noteEvents(blocks, style) +
		sectionEvents(headings, style) + brandEvents(data.Branding, result.Duration, style)

	if style.wordMode() {
		words := clipWords(wordCues(result, style), limit)

		var b strings.Builder

		writeHeader(&b, style, false)
		b.WriteString(wordEvents(words, style))

		return b.String() + extras, len(words)
	}

	timed := speechCues(data, style, headings, end, limit)
	layout := hookLayout{end: end, margin: style.hookSubtitlesMargin(style.hookLineCount(data.Hook)), notes: blocks}

	if style.Hook.HideSubtitles {
		layout.margin = 0
	}

	return renderASS(timed, style, data.Karaoke, layout) + extras, len(timed)
}

// speechCues are the subtitles of the phrase layout with their times: the tokens of the transcript (with the
// orthography of the style, sentences in small letters, without the words that read a drawn heading out), grouped into
// phrases (or the pieces of before), the hook's repeat dropped, timed, cut at the end card (limit), off the screen under
// the hook (hook.hide_subtitles), the question under a heading shown section.lead after it.
func speechCues(data *CreateSubtitlesData, style *Style, headings []noteBlock, hookEnd, limit float64) []cue {
	tokens := tokensFromTranscript(data.Transcript)
	if style.Layout.Orthography {
		tokens = fixOrthography(tokens)
	}

	if style.Layout.LowercaseSentences {
		tokens = lowerSentenceStarts(tokens)
	}

	tokens = markAccents(hideReadings(tokens, headings), data.Accents, style.Accent.Words)
	limits := layoutLimits{maxLines: style.Layout.MaxLines, maxLineChars: style.lineChars()}
	units := buildUnits(tokens, limits.maxLineChars)
	explicit := len(parsePhrases(data.Accents)) > 0

	var cues []cue

	timing := defaultTiming

	if p := &style.Layout.Phrases; p.Enabled {
		barriers := glueBarriers(headings, hookEnd)
		cues, timing = buildCues(phraseGroups(units, limits, p, barriers), style, explicit), phraseTiming(p)
	} else {
		cues = buildCues(groupCues(units, limits), style, explicit)
	}

	if style.Hook.HideRepeat {
		cues = hideHookRepeat(cues, data.Hook, hookEnd)
	}

	timed := clipCues(timeCuesWith(cues, timing), limit)

	if style.Hook.HideSubtitles {
		timed = hideUnderHook(timed, hookEnd)
	}

	return leadUnderSections(timed, headings, style.Section.Lead)
}

// glueBarriers are the moments no phrase is glued across: the end of the hook (a piece under it and one after it are
// apart, so the hook's repeat is recognized), the starts of the drawn headings and the times their notes were said.
func glueBarriers(headings []noteBlock, hookEnd float64) []float64 {
	barriers := make([]float64, 0, 2*len(headings)+1)
	if hookEnd > 0 {
		barriers = append(barriers, hookEnd)
	}

	for _, h := range headings {
		barriers = append(barriers, h.start)
		if h.at != h.start {
			barriers = append(barriers, h.at)
		}
	}

	return barriers
}

// titleEvents are the dialogue lines of the exercise titles: the Title style, above the subtitles, the text escaped
// and in capitals when the style says so. libass wraps a long title within the side margins.
func titleEvents(titles []Title, style *Style) string {
	var b strings.Builder

	for _, t := range titles {
		text := censor(strings.Join(strings.Fields(t.Text), " "))
		if style.Title.Uppercase {
			text = strings.ToUpper(text)
		}

		if text == "" || t.End <= t.Start {
			continue
		}

		fmt.Fprintf(&b, "Dialogue: %d,%s,%s,%s,,0,0,0,,%s\n", titleLayer, formatTime(t.Start), formatTime(t.End),
			titleStyleName, escapeText(text))
	}

	return b.String()
}

// titleStyleLine is the style of the exercise titles: on a translucent box when the style has one, otherwise with the
// outline and the shadow of the subtitles.
func titleStyleLine(style *Style) string {
	t := &style.Title
	alignment, marginV := placement(&Layout{
		Position: style.titlePosition(), MarginTop: t.Margin, MarginBottom: t.Margin,
	})

	borderStyle, outlineColor, outline := outlineBorderStyle, style.Outline.Color, style.Outline.Size
	shadowColor, shadow := style.Shadow.Color, style.Shadow.Size

	if t.Box != "" {
		borderStyle, outlineColor, outline, shadow = boxBorderStyle, t.Box, t.BoxPadding, 0
	}

	bold := 0
	if t.Bold {
		bold = assBold
	}

	fields := []string{
		titleStyleName, t.Font, strconv.Itoa(t.Size),
		assColor(t.Color), assColor(t.Color), assColor(outlineColor), assColor(shadowColor),
		strconv.Itoa(bold), "0", "0", "0", "100", "100", "0", "0", strconv.Itoa(borderStyle),
		formatFloat(outline), formatFloat(shadow),
		strconv.Itoa(alignment), strconv.Itoa(style.Layout.MarginSide), strconv.Itoa(style.Layout.MarginSide),
		strconv.Itoa(marginV), "1",
	}

	return "Style: " + strings.Join(fields, ",")
}

// renderASS writes an ASS document for a 1080×1920 frame. Line breaks are explicit (\N); WrapStyle 0 lets libass
// wrap a line only if the character estimate was too optimistic, so the text never leaves the frame. A subtitle that
// starts while the hook title is shown or meets a block of the screen notes stands clear of it for all its time (it
// does not jump while it is read), over the shade that extends the vignette under it (shadeEvents). A subtitle after a
// gap fades in over layout.fade_in (fadeIns).
func renderASS(cues []cue, style *Style, karaoke bool, hook hookLayout) string {
	margins := make([]int, 0, len(cues))
	for _, c := range cues {
		margins = append(margins, cueMargin(c, hook, style))
	}

	shade := shadeEvents(cues, margins, style, hook)

	var b strings.Builder

	writeHeader(&b, style, shade != "")
	b.WriteString(shade)

	fades := fadeIns(cues, style.Layout.FadeIn)

	for i, c := range cues {
		marginV := margins[i]

		if !karaoke {
			writeDialogue(&b, c.Start, c.End, marginV, fadePrefix(fades[i])+cueText(c, style, -1))

			continue
		}

		for j, e := range karaokeEvents(c, style) {
			fade := ""
			if j == 0 {
				fade = fadePrefix(fades[i])
			}

			writeDialogue(&b, e.start, e.end, marginV, fade+e.text)
		}
	}

	return b.String()
}

// writeHeader writes the script info, the styles (the Shade style of the drawings under the shifted subtitles when
// shaded) and the format line of the events.
func writeHeader(b *strings.Builder, style *Style, shaded bool) {
	b.WriteString("[Script Info]\n")
	b.WriteString("; Generated by ai-agents subtitles (T3.3)\n")
	b.WriteString("ScriptType: v4.00+\n")
	fmt.Fprintf(b, "PlayResX: %d\nPlayResY: %d\n", FrameWidth, FrameHeight)
	b.WriteString("WrapStyle: 0\nScaledBorderAndShadow: yes\nYCbCr Matrix: TV.709\n\n")

	b.WriteString("[V4+ Styles]\n" + styleFormat + "\n")
	b.WriteString(styleLine(mainStyleName, &style.Text, style) + "\n")
	b.WriteString(styleLine(accentStyleName, &style.Accent.TextStyle, style) + "\n")
	b.WriteString(titleStyleLine(style) + "\n")
	b.WriteString(hookStyleLine(style) + "\n")
	b.WriteString(wordStyleLine(style) + "\n")
	b.WriteString(brandStyleLines(style))
	b.WriteString(sectionStyleLine(style) + "\n" + topbarStyleLine(style) + "\n")

	if shaded {
		b.WriteString(shadeStyleLine(style) + "\n")
	}

	b.WriteString("\n[Events]\n" + eventFormat + "\n")
}

func styleLine(name string, t *TextStyle, style *Style) string {
	alignment, marginV := placement(&style.Layout)
	bold := 0

	if t.Bold {
		bold = assBold
	}

	fields := []string{
		name, t.Font, strconv.Itoa(t.Size),
		assColor(t.Color), assColor(t.Color), assColor(style.Outline.Color), assColor(style.Shadow.Color),
		strconv.Itoa(bold), "0", "0", "0", "100", "100", "0", "0", "1",
		formatFloat(style.Outline.Size), formatFloat(style.Shadow.Size),
		strconv.Itoa(alignment), strconv.Itoa(style.Layout.MarginSide), strconv.Itoa(style.Layout.MarginSide),
		strconv.Itoa(marginV), "1",
	}

	return "Style: " + strings.Join(fields, ",")
}

func placement(l *Layout) (alignment, marginV int) {
	switch l.Position {
	case BottomPosition:
		return bottomAlignment, l.MarginBottom
	case MiddlePosition:
		return middleAlignment, 0
	default:
		return topAlignment, l.MarginTop
	}
}

// writeDialogue writes a subtitle event; marginV 0 keeps the margin of the style.
func writeDialogue(b *strings.Builder, start, end float64, marginV int, text string) {
	fmt.Fprintf(b, "Dialogue: 0,%s,%s,%s,,0,0,%d,,%s\n", formatTime(start), formatTime(end), mainStyleName, marginV,
		text)
}

// cueText renders the lines of a subtitle; the token with index current (over all lines) gets the karaoke color.
func cueText(c cue, style *Style, current int) string {
	lines := make([]string, 0, len(c.Lines))
	index, styled, prevScale := 0, false, fullScaleX

	for _, line := range c.Lines {
		var text string

		scale := lineScale(line, style.lineChars())
		prefix := ""

		if scale != prevScale {
			prefix = fmt.Sprintf(`{\fscx%d}`, scale)
		}

		// Overrides last until the end of the event, across \N: styled carries them to the next line.
		text, styled = lineText(line, style, current-index, styled, scale)
		lines = append(lines, prefix+text)
		index += len(line)
		prevScale = scale
	}

	return strings.Join(lines, assLineBreak)
}

// lineText writes runs of words with the same look: accented words switch to the Accent style ({\rAccent}),
// the current karaoke word gets the karaoke color, {\r} returns to the line style. styled says whether the text
// before the line ended with overrides; the result says it for the end of the line.
func lineText(line []token, style *Style, current int, styled bool, scale int) (string, bool) {
	var b strings.Builder

	for i, t := range line {
		accent, highlighted := t.Accent != noAccent, i == current
		sameRun := i > 0 && (line[i-1].Accent != noAccent) == accent && i-1 != current && !highlighted

		if i > 0 {
			b.WriteByte(' ')
		}

		if !sameRun {
			b.WriteString(runTags(accent, highlighted, styled, scale, style))
			styled = accent || highlighted
		}

		text := t.Text
		if (accent && style.Accent.Uppercase) || (!accent && style.Text.Uppercase) {
			text = strings.ToUpper(text)
		}

		b.WriteString(escapeText(text))
	}

	return b.String(), styled
}

func runTags(accent, highlighted, styled bool, scale int, style *Style) string {
	var tags string

	switch {
	case accent:
		tags = `\r` + accentStyleName
	case styled:
		tags = `\r`
	}

	// \r resets the line's horizontal scale too.
	if tags != "" && scale != fullScaleX {
		tags += fmt.Sprintf(`\fscx%d`, scale)
	}

	if highlighted {
		tags += `\1c` + assOverrideColor(style.KaraokeColor) + `\1a` + assOverrideAlpha(style.KaraokeColor)
	}

	if tags == "" {
		return ""
	}

	return "{" + tags + "}"
}

type event struct {
	start float64
	end   float64
	text  string
}

// karaokeEvents shows a subtitle as consecutive events, one per spoken word, each with that word highlighted.
// ASS \k cannot be used for this: it colors a word once it is reached and never returns it to the normal color.
func karaokeEvents(c cue, style *Style) []event {
	tokens := c.tokens()
	events := make([]event, 0, len(tokens))
	start := c.Start

	for i := range tokens {
		end := c.End
		if i+1 < len(tokens) {
			end = min(max(roundCS(tokens[i+1].Start), start), c.End)
		}

		if end > start {
			events = append(events, event{start: start, end: end, text: cueText(c, style, i)})
			start = end
		}
	}

	return events
}

// escapeText keeps spoken text from being read as ASS markup: braces would start an override block (libass
// renders \{ and \} as literal braces), a backslash would start \N, \n or \h — it is dropped, speech has none
// (tokensFromTranscript drops it too). Control characters become spaces.
func escapeText(s string) string {
	var b strings.Builder

	for _, r := range s {
		switch {
		case r == '{' || r == '}':
			b.WriteRune('\\')
			b.WriteRune(r)
		case r == '\\':
		case unicode.IsControl(r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}

	return b.String()
}

// lineScale squeezes a line that is longer than the limit (a single long word) so it stays within the margins.
func lineScale(line []token, maxChars int) int {
	width := len(line) - 1
	for _, t := range line {
		width += utf8.RuneCountInString(t.Text)
	}

	if width <= maxChars {
		return fullScaleX
	}

	return max(percentBase*maxChars/width, minScaleX)
}

// formatTime writes seconds as ASS H:MM:SS.CC.
func formatTime(seconds float64) string {
	cs := int(math.Round(max(seconds, 0) * centisecondsPerSecond))

	return fmt.Sprintf("%d:%02d:%02d.%02d",
		cs/csPerHour, cs%csPerHour/csPerMinute, cs%csPerMinute/centisecondsPerSecond, cs%centisecondsPerSecond)
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, assFloatFormat, -1, float64Bits)
}

// assOverrideColor is the color for a \1c override, &HBBGGRR&: overrides set opacity separately.
func assOverrideColor(color string) string {
	c := assColor(color) // &HAABBGGRR

	return "&H" + c[len("&HAA"):] + "&"
}

// assOverrideAlpha is the transparency for a \1a override, &HAA&.
func assOverrideAlpha(color string) string {
	c := assColor(color) // &HAABBGGRR

	return c[:len("&HAA")] + "&"
}

// clipWords drops the words that start at limit or later and ends the others there; limit 0 keeps them all.
func clipWords(words []wordCue, limit float64) []wordCue {
	if limit <= 0 {
		return words
	}

	out := make([]wordCue, 0, len(words))

	for _, w := range words {
		if w.Start < limit {
			w.End = min(w.End, limit)
			out = append(out, w)
		}
	}

	return out
}

// clipCues drops the subtitles that start at limit or later and ends the others there; limit 0 keeps them all.
func clipCues(cues []cue, limit float64) []cue {
	if limit <= 0 {
		return cues
	}

	out := make([]cue, 0, len(cues))

	for _, c := range cues {
		if c.Start < limit {
			c.End = min(c.End, limit)
			out = append(out, c)
		}
	}

	return out
}
