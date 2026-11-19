package extract

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// Limits for untrusted PDFs. The library trusts the file: it allocates by /Count, follows /Kids
// cycles forever and inflates streams without bounds, so the page tree is walked here and every
// stream is inflated through a limit before the library reads it.
const (
	PDFTimeout = 30 * time.Second // the whole document

	maxPDFPages         = 1000    // further pages are ignored
	maxPageTreeNodes    = 5000    // nodes visited while walking /Kids: stops cycles
	maxPageTreeDepth    = 32      // nesting of /Pages nodes
	maxStreamBytes      = 4 << 20 // inflated size of one content or ToUnicode stream
	maxPredictorColumns = 1 << 16 // the library allocates a buffer of this size per stream
	maxGlyphsPerPage    = 200_000
	maxGlyphsPerPDF     = 2_000_000
)

// Layout thresholds relative to the font size. The library's own text helpers glue words together
// when a document mixes fonts (common for Cyrillic: letters and punctuation come from different
// subsets), so lines are rebuilt from glyph positions (see pdfcontent.go).
const (
	sameLineRatio     = 0.5  // glyphs whose baselines differ by less than this belong to one line
	wordGapRatio      = 0.15 // a horizontal gap wider than this is a space
	paragraphGapRatio = 1.8  // a vertical gap wider than this starts a new paragraph
)

var errPDFLimit = errors.New("pdf exceeds extraction limits")

// pdfText extracts the text layer with a timeout. The library cannot be interrupted, so it runs in
// a goroutine: on timeout the caller gets an error, but the goroutine runs until the library returns.
// The limits above bound the memory it can take; a long-running process should move PDF parsing
// to a separate process before accepting PDFs from untrusted users.
func pdfText(ctx context.Context, data []byte) (string, error) {
	return runWithTimeout(ctx, PDFTimeout, func() (string, error) {
		return parsePDF(data)
	})
}

// runWithTimeout runs fn in a goroutine and stops waiting for it when the timeout or ctx expires.
// A panic in fn becomes ErrInvalidDocument: the library panics on many malformed files.
func runWithTimeout(ctx context.Context, timeout time.Duration, fn func() (string, error)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		text string
		err  error
	}

	done := make(chan result, 1) // buffered: a late result must not block the goroutine forever

	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("failed to parse pdf: %v: %w", r, ErrInvalidDocument)}
			}
		}()

		text, err := fn()
		done <- result{text: text, err: err}
	}()

	select {
	case r := <-done:
		return r.text, r.err
	case <-ctx.Done():
		return "", fmt.Errorf("failed to parse pdf in %s: %w: %w", timeout, ErrInvalidDocument, ctx.Err())
	}
}

// parsePDF extracts the text layer page by page. Scanned PDFs have no text layer and give an error:
// they need OCR first. Columns and tables are not reconstructed: lines are read top to bottom.
func parsePDF(data []byte) (string, error) {
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("failed to open pdf: %w: %w", ErrInvalidDocument, err)
	}

	pages, err := collectPages(reader.Trailer().Key("Root").Key("Pages"))
	if err != nil {
		return "", err
	}

	var (
		texts  []string
		glyphs int
	)

	for i, page := range pages {
		if err := checkPageStreams(page); err != nil {
			return "", fmt.Errorf("pdf page %d: %w", i+1, err)
		}

		pageGlyphs, err := readPageGlyphs(page, min(maxGlyphsPerPage, maxGlyphsPerPDF-glyphs))
		if err != nil {
			return "", fmt.Errorf("pdf page %d: %w", i+1, err)
		}

		glyphs += len(pageGlyphs)

		if text := layoutText(pageGlyphs); text != "" {
			texts = append(texts, text)
		}
	}

	if len(texts) == 0 {
		return "", fmt.Errorf("pdf has no text layer (scanned document?): %w", ErrInvalidDocument)
	}

	return strings.Join(texts, "\n\n"), nil
}

type pageTreeNode struct {
	v     pdf.Value
	depth int
}

// collectPages walks the page tree depth-first in document order, ignoring /Count. At most
// maxPDFPages pages are returned; a tree with more nodes or deeper nesting (a /Kids cycle) is an error.
func collectPages(root pdf.Value) ([]pdf.Page, error) {
	var pages []pdf.Page

	stack := []pageTreeNode{{v: root}}

	for visited := 0; len(stack) > 0 && len(pages) < maxPDFPages; visited++ {
		if visited >= maxPageTreeNodes {
			return nil, fmt.Errorf("page tree has more than %d nodes (cyclic /Kids?): %w: %w",
				maxPageTreeNodes, ErrInvalidDocument, errPDFLimit)
		}

		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		switch node.v.Key("Type").Name() {
		case "Page":
			pages = append(pages, pdf.Page{V: node.v})
		case "Pages":
			if node.depth >= maxPageTreeDepth {
				return nil, fmt.Errorf("page tree is deeper than %d: %w: %w", maxPageTreeDepth, ErrInvalidDocument, errPDFLimit)
			}

			kids := node.v.Key("Kids")
			if kids.Len() > maxPageTreeNodes {
				return nil, fmt.Errorf("page tree node has %d kids: %w: %w", kids.Len(), ErrInvalidDocument, errPDFLimit)
			}

			// Reverse order on the stack, so pages come out in document order.
			for i := kids.Len() - 1; i >= 0; i-- {
				stack = append(stack, pageTreeNode{v: kids.Index(i), depth: node.depth + 1})
			}
		}
	}

	if len(pages) == 0 {
		return nil, fmt.Errorf("pdf has no pages: %w", ErrInvalidDocument)
	}

	return pages, nil
}

// checkPageStreams inflates the streams the library will read for the page (content and fonts'
// ToUnicode maps) through a limit, so it never inflates an oversized stream itself.
func checkPageStreams(page pdf.Page) error {
	contents := page.V.Key("Contents")

	streams := []pdf.Value{contents}
	if contents.Kind() == pdf.Array {
		if contents.Len() > maxPageTreeNodes {
			return fmt.Errorf("page has %d content streams: %w: %w", contents.Len(), ErrInvalidDocument, errPDFLimit)
		}

		streams = streams[:0]
		for i := range contents.Len() {
			streams = append(streams, contents.Index(i))
		}
	}

	for _, name := range page.Fonts() {
		streams = append(streams, page.Font(name).V.Key("ToUnicode"))
	}

	for _, s := range streams {
		if err := checkStream(s); err != nil {
			return err
		}
	}

	return nil
}

func checkStream(s pdf.Value) error {
	if s.Kind() != pdf.Stream {
		return nil
	}

	if columns := s.Key("DecodeParms").Key("Columns").Int64(); columns > maxPredictorColumns {
		return fmt.Errorf("stream predictor has %d columns: %w: %w", columns, ErrInvalidDocument, errPDFLimit)
	}

	rc := s.Reader()
	defer rc.Close()

	n, err := io.Copy(io.Discard, io.LimitReader(rc, maxStreamBytes+1))
	if err != nil {
		return fmt.Errorf("failed to inflate pdf stream: %w: %w", ErrInvalidDocument, err)
	}

	if n > maxStreamBytes {
		return fmt.Errorf("pdf stream inflates to more than %d bytes: %w: %w", maxStreamBytes, ErrInvalidDocument, errPDFLimit)
	}

	return nil
}

type pdfLine struct {
	y        float64
	fontSize float64
	glyphs   []glyph
}

// layoutText rebuilds lines and paragraphs of one page from positioned glyphs.
func layoutText(glyphs []glyph) string {
	lines := groupLines(glyphs)

	var sb strings.Builder

	for i, line := range lines {
		if i > 0 {
			sb.WriteByte('\n')

			if lines[i-1].y-line.y > paragraphGapRatio*max(line.fontSize, lines[i-1].fontSize) {
				sb.WriteByte('\n')
			}
		}

		sb.WriteString(lineText(line))
	}

	return strings.TrimSpace(sb.String())
}

// groupLines puts glyphs with close baselines into lines, ordered top to bottom.
func groupLines(glyphs []glyph) []pdfLine {
	visible := make([]glyph, 0, len(glyphs))
	for _, g := range glyphs {
		if g.text != "" && g.text != string(utf8.RuneError) {
			visible = append(visible, g)
		}
	}

	// PDF y grows upwards: higher lines first.
	slices.SortStableFunc(visible, func(a, b glyph) int { return cmp.Compare(b.y, a.y) })

	var lines []pdfLine

	for _, g := range visible {
		if n := len(lines); n > 0 && math.Abs(lines[n-1].y-g.y) < sameLineRatio*max(g.fontSize, lines[n-1].fontSize) {
			last := lines[n-1]
			last.glyphs = append(last.glyphs, g)
			last.fontSize = max(last.fontSize, g.fontSize)
			lines[n-1] = last

			continue
		}

		lines = append(lines, pdfLine{y: g.y, fontSize: g.fontSize, glyphs: []glyph{g}})
	}

	return lines
}

// lineText joins glyphs of a line left to right, adding spaces at gaps between words.
func lineText(line pdfLine) string {
	glyphs := slices.Clone(line.glyphs)
	slices.SortStableFunc(glyphs, func(a, b glyph) int { return cmp.Compare(a.x, b.x) })

	var (
		sb  strings.Builder
		end float64 // right edge of the previous glyph
	)

	for i, g := range glyphs {
		if i > 0 && g.x-end > wordGapRatio*g.fontSize && !strings.HasSuffix(sb.String(), " ") {
			sb.WriteByte(' ')
		}

		sb.WriteString(g.text)
		end = max(end, g.x+g.width)
	}

	// Bullets of lists are private-use glyphs of symbol fonts.
	text := strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Co, r) {
			return '•'
		}

		return r
	}, sb.String())

	return strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " ")
}
