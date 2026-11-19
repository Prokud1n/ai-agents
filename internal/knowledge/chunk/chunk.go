// Package chunk splits document text into chunks for embedding: by markdown headings first,
// then by paragraphs, sentences and words. All functions are pure.
package chunk

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Sizes are in runes. Tokens are estimated as runes / RunesPerToken: about 4 runes per token
// for English and 2–2.5 for Russian, so 3 is a middle ground and the target of 2000 runes
// is roughly 500–800 tokens.
const (
	TargetRunes   = 2000 // a chunk is closed once the next piece would push it past this size
	OverlapRunes  = 300  // tail of the previous chunk repeated at the start of the next one in the same section
	RunesPerToken = 3
	// MaxHeadingRunes bounds the heading path kept in a chunk: top levels are dropped first.
	MaxHeadingRunes = 200

	// Version changes whenever Split cuts the same text differently: stored documents chunked
	// by another version are re-ingested.
	Version = 1

	headingSeparator = " > "
	paragraphSep     = "\n\n"
	maxHeadingLevel  = 6
	ellipsis         = "…"
	codeFence        = "```"
)

var (
	headingRe  = regexp.MustCompile(`^(#{1,6})[ \t]+(.+?)[ \t#]*$`)
	sentenceRe = regexp.MustCompile(`[.!?…]+["»”)]*\s+`)
	blankRunRe = regexp.MustCompile(`\n{3,}`)
)

// Chunk is a piece of a document ready for embedding.
type Chunk struct {
	Heading string // path of section headings, e.g. "Питание > Белок"; empty before the first heading
	Text    string // heading path (if any) and the body: the heading is kept as context for search
	Tokens  int    // estimated token count of Text
}

type section struct {
	heading    string
	paragraphs []string
}

// Split cuts text into chunks of about TargetRunes runes: sections never share a chunk,
// a long section is split by paragraphs (then sentences, then words) with OverlapRunes of overlap.
func Split(text string) []Chunk {
	sections := splitSections(Normalize(text))

	chunks := make([]Chunk, 0, len(sections))
	for _, s := range sections {
		for _, body := range packParagraphs(s.paragraphs, TargetRunes, OverlapRunes) {
			chunks = append(chunks, newChunk(s.heading, body))
		}
	}

	return chunks
}

// Title returns the text of the first level-1 markdown heading, or an empty string.
func Title(text string) string {
	for line := range strings.SplitSeq(Normalize(text), "\n") {
		m := headingRe.FindStringSubmatch(line)
		if len(m) > 1 && len(m[1]) == 1 {
			return strings.TrimSpace(m[2])
		}
	}

	return ""
}

// EstimateTokens approximates the token count of text.
func EstimateTokens(text string) int {
	n := utf8.RuneCountInString(text)

	return (n + RunesPerToken - 1) / RunesPerToken
}

// Normalize unifies line endings, drops trailing spaces and BOM and collapses runs of blank lines.
func Normalize(text string) string {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, unicode.IsSpace)
	}

	text = strings.Join(lines, "\n")
	text = blankRunRe.ReplaceAllString(text, paragraphSep)

	return strings.TrimSpace(text)
}

func newChunk(heading, body string) Chunk {
	text := body
	if heading != "" {
		text = heading + paragraphSep + body
	}

	return Chunk{Heading: heading, Text: text, Tokens: EstimateTokens(text)}
}

// splitSections groups paragraphs under markdown headings. Lines inside ``` fences are never headings;
// an unclosed fence is plain text, so it does not hide the headings after it.
func splitSections(text string) []section {
	var (
		sections []section
		current  section
		stack    [maxHeadingLevel]string
		para     []string
		inFence  bool
	)

	lines := strings.Split(text, "\n")
	lastFence := unmatchedFence(lines)

	flushPara := func() {
		if p := strings.TrimSpace(strings.Join(para, "\n")); p != "" {
			current.paragraphs = append(current.paragraphs, p)
		}

		para = para[:0]
	}

	for i, line := range lines {
		if isFence(line) && i != lastFence {
			inFence = !inFence
		}

		if m := headingRe.FindStringSubmatch(line); m != nil && !inFence {
			flushPara()

			if len(current.paragraphs) > 0 {
				sections = append(sections, current)
			}

			level := len(m[1])
			stack[level-1] = strings.TrimSpace(m[2])
			clear(stack[level:])
			current = section{heading: headingPath(stack[:level])}

			continue
		}

		if line == "" && !inFence {
			flushPara()

			continue
		}

		para = append(para, line)
	}

	flushPara()

	if len(current.paragraphs) > 0 {
		sections = append(sections, current)
	}

	return sections
}

func isFence(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), codeFence)
}

// unmatchedFence returns the index of the last fence line if fences are unbalanced, otherwise -1.
func unmatchedFence(lines []string) int {
	last, count := -1, 0

	for i, line := range lines {
		if isFence(line) {
			last = i
			count++
		}
	}

	if count%2 == 0 {
		return -1
	}

	return last
}

// headingPath joins non-empty heading levels, dropping top levels while the path is longer than
// MaxHeadingRunes; a single heading that is still too long is cut.
func headingPath(levels []string) string {
	parts := make([]string, 0, len(levels))
	for _, l := range levels {
		if l != "" {
			parts = append(parts, l)
		}
	}

	for len(parts) > 1 && utf8.RuneCountInString(strings.Join(parts, headingSeparator)) > MaxHeadingRunes {
		parts = parts[1:]
	}

	path := strings.Join(parts, headingSeparator)
	if runes := []rune(path); len(runes) > MaxHeadingRunes {
		path = string(runes[:MaxHeadingRunes-1]) + ellipsis
	}

	return path
}

// packParagraphs joins paragraphs into bodies of about target runes. When a body is closed, the next one
// starts with up to overlap runes of its tail. Paragraphs longer than target are split first.
func packParagraphs(paragraphs []string, target, overlap int) []string {
	var (
		bodies  []string
		current []string
		size    int
		hasNew  bool // current holds more than the overlap carried from the previous body
	)

	for _, p := range paragraphs {
		for _, piece := range splitLong(p, target) {
			pieceSize := utf8.RuneCountInString(piece)

			if hasNew && size+len(paragraphSep)+pieceSize > target {
				body := strings.Join(current, paragraphSep)
				bodies = append(bodies, body)

				current, size = nil, 0
				if tail := tailWords(body, overlap); tail != "" {
					current = append(current, tail)
					size = utf8.RuneCountInString(tail)
				}
			}

			if len(current) > 0 {
				size += len(paragraphSep)
			}

			current = append(current, piece)
			size += pieceSize
			hasNew = true
		}
	}

	if hasNew {
		bodies = append(bodies, strings.Join(current, paragraphSep))
	}

	return bodies
}

// splitLong splits text longer than limit runes into pieces of at most limit runes:
// by sentences, then by words, then (for a single huge word) by runes.
func splitLong(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}

	var units []string
	for _, s := range splitSentences(text) {
		if utf8.RuneCountInString(s) <= limit {
			units = append(units, s)

			continue
		}

		for _, w := range strings.Fields(s) {
			units = append(units, splitRunes(w, limit)...)
		}
	}

	return packUnits(units, limit)
}

// packUnits greedily joins units with spaces into pieces of at most limit runes.
func packUnits(units []string, limit int) []string {
	var (
		pieces  []string
		current strings.Builder
		size    int
	)

	for _, u := range units {
		n := utf8.RuneCountInString(u)
		if size > 0 && size+1+n > limit {
			pieces = append(pieces, current.String())
			current.Reset()

			size = 0
		}

		if size > 0 {
			current.WriteByte(' ')

			size++
		}

		current.WriteString(u)
		size += n
	}

	if size > 0 {
		pieces = append(pieces, current.String())
	}

	return pieces
}

func splitSentences(text string) []string {
	var sentences []string

	start := 0
	for _, loc := range sentenceRe.FindAllStringIndex(text, -1) {
		if s := strings.TrimSpace(text[start:loc[1]]); s != "" {
			sentences = append(sentences, s)
		}

		start = loc[1]
	}

	if s := strings.TrimSpace(text[start:]); s != "" {
		sentences = append(sentences, s)
	}

	return sentences
}

func splitRunes(word string, limit int) []string {
	runes := []rune(word)
	if len(runes) <= limit {
		return []string{word}
	}

	parts := make([]string, 0, len(runes)/limit+1)
	for start := 0; start < len(runes); start += limit {
		parts = append(parts, string(runes[start:min(start+limit, len(runes))]))
	}

	return parts
}

// tailWords returns the end of text of at most limit runes, starting at a word boundary.
func tailWords(text string, limit int) string {
	if limit <= 0 {
		return ""
	}

	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}

	tail := runes[len(runes)-limit:]

	// Drop the leading partial word unless the cut happens to be at a boundary.
	if !unicode.IsSpace(runes[len(runes)-limit-1]) {
		idx := strings.IndexFunc(string(tail), unicode.IsSpace)
		if idx < 0 {
			return ""
		}

		return strings.TrimSpace(string(tail)[idx:])
	}

	return strings.TrimSpace(string(tail))
}
