package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const (
	wordNS            = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	docxDocumentPath  = "word/document.xml"
	docxStylesPath    = "word/styles.xml"
	maxDocxPartBytes  = 64 << 20 // guards against zip bombs: brand documents are far smaller
	maxHeadingLevel   = 6
	titleHeadingLevel = 1
	listItemPrefix    = "- "
)

// Built-in style names are English in every Word locale ("heading 1"), style IDs are not ("1", "Heading1").
var (
	headingStyleRe = regexp.MustCompile(`(?i)^heading\s*([1-9])$`)
	listStyleRe    = regexp.MustCompile(`(?i)^list\s*(bullet|number|paragraph)`)
)

// docxStyles holds what the extractor needs from word/styles.xml, keyed by style ID.
type docxStyles struct {
	headings map[string]int
	lists    map[string]bool
}

// docxText reads paragraphs from word/document.xml. Heading styles become "#" markdown headings,
// list paragraphs get a "- " prefix, table cells become separate paragraphs.
func docxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("failed to open docx archive: %w: %w", ErrInvalidDocument, err)
	}

	styles, err := docxReadStyles(zr)
	if err != nil {
		return "", err
	}

	doc, err := readZipPart(zr, docxDocumentPath)
	if err != nil {
		return "", err
	}

	if doc == nil {
		return "", fmt.Errorf("docx has no %s: %w", docxDocumentPath, ErrInvalidDocument)
	}

	paragraphs, err := docxParagraphs(doc, styles)
	if err != nil {
		return "", err
	}

	return strings.Join(paragraphs, "\n\n"), nil
}

type docxParagraph struct {
	text   strings.Builder
	style  string
	isList bool
}

func (p *docxParagraph) render(styles docxStyles) string {
	text := strings.TrimSpace(p.text.String())
	if text == "" {
		return ""
	}

	if level := styles.headingLevel(p.style); level > 0 {
		return strings.Repeat("#", level) + " " + strings.Join(strings.Fields(text), " ")
	}

	if p.isList || styles.lists[p.style] || listStyleRe.MatchString(p.style) {
		return listItemPrefix + text
	}

	return text
}

// docxReader collects paragraphs. Paragraphs nest (a text box inside a paragraph holds its own
// w:p), so open paragraphs form a stack: an inner one is emitted when it closes, and the outer one
// keeps its text.
type docxReader struct {
	styles     docxStyles
	paragraphs []string
	open       []*docxParagraph
	inText     bool
}

func docxParagraphs(doc []byte, styles docxStyles) ([]string, error) {
	r := &docxReader{styles: styles}
	dec := xml.NewDecoder(bytes.NewReader(doc))

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return r.paragraphs, nil
		}

		if err != nil {
			return nil, fmt.Errorf("failed to parse docx xml: %w: %w", ErrInvalidDocument, err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			r.start(t)
		case xml.EndElement:
			r.end(t)
		case xml.CharData:
			if p := r.current(); r.inText && p != nil {
				p.text.Write(t)
			}
		}
	}
}

func (r *docxReader) current() *docxParagraph {
	if len(r.open) == 0 {
		return nil
	}

	return r.open[len(r.open)-1]
}

func (r *docxReader) start(el xml.StartElement) {
	if el.Name.Space != wordNS {
		return
	}

	if el.Name.Local == "p" {
		r.open = append(r.open, &docxParagraph{})

		return
	}

	r.inText = el.Name.Local == "t"
	applyRunElement(r.current(), el)
}

func (r *docxReader) end(el xml.EndElement) {
	if el.Name.Space != wordNS {
		return
	}

	r.inText = false

	p := r.current()
	if el.Name.Local != "p" || p == nil {
		return
	}

	r.open = r.open[:len(r.open)-1]

	if s := p.render(r.styles); s != "" {
		r.paragraphs = append(r.paragraphs, s)
	}
}

// applyRunElement handles elements inside a paragraph that change its text or style.
func applyRunElement(p *docxParagraph, el xml.StartElement) {
	if p == nil {
		return
	}

	switch el.Name.Local {
	case "tab":
		p.text.WriteByte('\t')
	case "br", "cr":
		p.text.WriteByte('\n')
	case "pStyle":
		p.style = wordAttr(el, "val")
	case "numPr":
		p.isList = true
	}
}

// docxReadStyles finds heading and list styles in word/styles.xml; without it the style IDs
// themselves are matched ("Heading1").
func docxReadStyles(zr *zip.Reader) (docxStyles, error) {
	styles := docxStyles{headings: make(map[string]int), lists: make(map[string]bool)}

	data, err := readZipPart(zr, docxStylesPath)
	if err != nil || data == nil {
		return styles, err
	}

	dec := xml.NewDecoder(bytes.NewReader(data))

	var styleID string

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return styles, nil
		}

		if err != nil {
			return docxStyles{}, fmt.Errorf("failed to parse docx styles: %w: %w", ErrInvalidDocument, err)
		}

		el, ok := tok.(xml.StartElement)
		if !ok || el.Name.Space != wordNS {
			continue
		}

		switch el.Name.Local {
		case "style":
			styleID = wordAttr(el, "styleId")
			styles.add(styleID, styleID)
		case "name":
			styles.add(styleID, wordAttr(el, "val"))
		}
	}
}

// add records the style if its name or ID marks a heading or a list.
func (s docxStyles) add(styleID, name string) {
	if styleID == "" {
		return
	}

	if level := headingLevel(name); level > 0 {
		s.headings[styleID] = level
	}

	if listStyleRe.MatchString(name) {
		s.lists[styleID] = true
	}
}

// headingLevel looks the style ID up in styles.xml, falling back to the ID itself ("Heading2").
func (s docxStyles) headingLevel(styleID string) int {
	if level, ok := s.headings[styleID]; ok {
		return level
	}

	return headingLevel(styleID)
}

// headingLevel returns the markdown level for a style name or ID, 0 if it is not a heading.
func headingLevel(style string) int {
	if strings.EqualFold(style, "title") {
		return titleHeadingLevel
	}

	m := headingStyleRe.FindStringSubmatch(style)
	if m == nil {
		return 0
	}

	level, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}

	return min(level, maxHeadingLevel)
}

// readZipPart returns the content of a file in the archive, or nil if there is no such file.
func readZipPart(zr *zip.Reader, name string) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("failed to open %s: %w: %w", name, ErrInvalidDocument, err)
		}

		data, err := io.ReadAll(io.LimitReader(rc, maxDocxPartBytes+1))

		if closeErr := rc.Close(); err == nil {
			err = closeErr
		}

		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w: %w", name, ErrInvalidDocument, err)
		}

		if len(data) > maxDocxPartBytes {
			return nil, fmt.Errorf("%s is larger than %d bytes: %w", name, maxDocxPartBytes, ErrInvalidDocument)
		}

		return data, nil
	}

	return nil, nil
}

func wordAttr(el xml.StartElement, name string) string {
	for _, a := range el.Attr {
		if a.Name.Local == name && (a.Name.Space == wordNS || a.Name.Space == "") {
			return a.Value
		}
	}

	return ""
}
