package extract

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// Limits for untrusted PDFs. The library trusts the file: it allocates by /Count, follows /Kids
// cycles forever and inflates streams without bounds, so the page tree is walked here and every
// stream is inflated through the remaining budget before the library reads it. These limits keep
// a valid-looking hostile file cheap; what they cannot stop (a crash inside the library, such as
// endless recursion) is contained by running the parser in a child process (pdfworker.go).
const (
	maxPDFPages         = 1000    // further pages are ignored
	maxPageTreeNodes    = 5000    // nodes visited while walking /Kids: stops cycles
	maxPageTreeDepth    = 32      // nesting of /Pages nodes
	maxPageStreamBytes  = 1 << 20 // inflated content and ToUnicode of one page; a stream referenced twice counts twice
	maxPDFStreamBytes   = 16 << 20
	maxPredictorColumns = 1 << 16 // the library allocates a buffer of this size per stream
	maxGlyphsPerPage    = 200_000
	maxGlyphsPerPDF     = 1_000_000 // as many characters as a knowledge base document may have
	// Entries of one ToUnicode CMap: the library decodes every code by scanning all of them, so a huge
	// CMap makes decoding quadratic. Real subset fonts have hundreds.
	maxCMapEntries = 4096
	// Hex strings of a CMap: two per bfchar entry, three per bfrange; a few more for the header.
	maxCMapHexStrings = 3*maxCMapEntries + 64
)

// Layout thresholds relative to the font size. The library's own text helpers glue words together
// when a document mixes fonts (common for Cyrillic: letters and punctuation come from different
// subsets), so lines are rebuilt from glyph positions (see pdfcontent.go).
const (
	sameLineRatio     = 0.5  // glyphs whose baselines differ by less than this belong to one line
	wordGapRatio      = 0.15 // a horizontal gap wider than this is a space
	paragraphGapRatio = 1.8  // a vertical gap wider than this starts a new paragraph
)

// errPDFLimit is an invalid document: errors.Is(err, ErrInvalidDocument) holds for it.
var errPDFLimit = fmt.Errorf("pdf exceeds extraction limits: %w", ErrInvalidDocument)

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
		budget = streamBudget{document: maxPDFStreamBytes}
	)

	for i, page := range pages {
		if err := budget.checkPage(page); err != nil {
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
			return nil, fmt.Errorf("page tree has more than %d nodes (cyclic /Kids?): %w",
				maxPageTreeNodes, errPDFLimit)
		}

		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		switch node.v.Key("Type").Name() {
		case "Page":
			pages = append(pages, pdf.Page{V: node.v})
		case "Pages":
			if node.depth >= maxPageTreeDepth {
				return nil, fmt.Errorf("page tree is deeper than %d: %w", maxPageTreeDepth, errPDFLimit)
			}

			kids := node.v.Key("Kids")
			if kids.Len() > maxPageTreeNodes {
				return nil, fmt.Errorf("page tree node has %d kids: %w", kids.Len(), errPDFLimit)
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

// streamBudget limits the inflated size of the streams the library will read: per page and for
// the whole document. Every reference is counted, so one stream listed many times in /Contents
// costs as much as distinct streams of that size.
type streamBudget struct {
	document int64 // bytes left for the rest of the document
	page     int64 // bytes left for the current page
}

// checkPage inflates the streams the library will read for the page (content and fonts' ToUnicode
// maps) through the remaining budget, so the library never inflates more than the budget allows.
func (b *streamBudget) checkPage(page pdf.Page) error {
	b.page = min(maxPageStreamBytes, b.document)

	contents := page.V.Key("Contents")

	streams := []pdf.Value{contents}
	if contents.Kind() == pdf.Array {
		if contents.Len() > maxPageTreeNodes {
			return fmt.Errorf("page has %d content streams: %w", contents.Len(), errPDFLimit)
		}

		streams = streams[:0]
		for i := range contents.Len() {
			streams = append(streams, contents.Index(i))
		}
	}

	cmaps := make([]pdf.Value, 0, len(page.Fonts()))
	for _, name := range page.Fonts() {
		cmaps = append(cmaps, page.Font(name).V.Key("ToUnicode"))
	}

	for i, s := range append(streams, cmaps...) {
		isCMap := i >= len(streams)

		n, hexStrings, err := inflateLimited(s, b.page, isCMap)
		if err != nil {
			return err
		}

		if hexStrings > maxCMapHexStrings {
			return fmt.Errorf("font ToUnicode map has more than %d entries: %w", maxCMapEntries, errPDFLimit)
		}

		b.page -= n
		b.document -= n
	}

	return nil
}

// hexCounter counts '<' bytes: hex strings (and a few dictionary delimiters) of a CMap.
type hexCounter struct {
	n int64
}

func (c *hexCounter) Write(p []byte) (int, error) {
	c.n += int64(bytes.Count(p, []byte{'<'}))

	return len(p), nil
}

// inflateLimited reads the inflated stream and returns its size (and, for a CMap, its count of hex
// strings); more than limit bytes is an error.
func inflateLimited(s pdf.Value, limit int64, countHex bool) (size, hexStrings int64, err error) {
	if s.Kind() != pdf.Stream {
		return 0, 0, nil
	}

	if columns := s.Key("DecodeParms").Key("Columns").Int64(); columns > maxPredictorColumns {
		return 0, 0, fmt.Errorf("stream predictor has %d columns: %w", columns, errPDFLimit)
	}

	rc := s.Reader()
	defer rc.Close()

	var (
		sink    io.Writer = io.Discard
		counter hexCounter
	)

	if countHex {
		sink = &counter
	}

	n, err := io.Copy(sink, io.LimitReader(rc, limit+1))
	if err != nil {
		return 0, 0, fmt.Errorf("failed to inflate pdf stream: %w: %w", ErrInvalidDocument, err)
	}

	if n > limit {
		return 0, 0, fmt.Errorf("pdf streams inflate to more than %d bytes per page or %d per document: %w",
			maxPageStreamBytes, maxPDFStreamBytes, errPDFLimit)
	}

	return n, counter.n, nil
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
