package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixtures: testdata/brand.docx is made with python-docx (Word styles "Heading 1/2", "List Bullet",
// a table); testdata/brand.pdf is the same document exported by LibreOffice.

const fixtureDocxText = `# Голос бренда

Говорим с подписчицами на «ты», тепло и без давления.

## Питание

Как мы говорим о питании: без запретов и чувства вины.

- Белок в каждом приёме пищи

- Вода до и после тренировки

Продукт

Марафон 21 день`

const fixturePDFText = `Голос бренда
Говорим с подписчицами на «ты», тепло и без давления.

Питание
Как мы говорим о питании: без запретов и чувства вины.

• Белок в каждом приёме пищи
• Вода до и после тренировки

Продукт Марафон 21 день`

func TestText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fileName  string
		input     []byte
		expected  string
		expectErr error
	}{
		{name: "markdown", fileName: "voice.md", input: []byte("# Голос\n\nтекст"), expected: "# Голос\n\nтекст"},
		{name: "txt with BOM", fileName: "notes.TXT", input: []byte("\ufeffзаметки"), expected: "заметки"},
		{name: "txt not in UTF-8", fileName: "cp1251.txt", input: []byte{0xcf, 0xf0, 0xe8}, expectErr: ErrInvalidDocument},
		{name: "docx fixture", fileName: "brand.docx", input: readFixture(t, "brand.docx"), expected: fixtureDocxText},
		{name: "pdf fixture", fileName: "brand.pdf", input: readFixture(t, "brand.pdf"), expected: fixturePDFText},
		{name: "broken docx", fileName: "broken.docx", input: []byte("not a zip"), expectErr: ErrInvalidDocument},
		{name: "broken pdf", fileName: "broken.pdf", input: []byte("%PDF-1.4 garbage"), expectErr: ErrInvalidDocument},
		{name: "too large", fileName: "big.txt", input: make([]byte, MaxFileBytes+1), expectErr: ErrTooLarge},
		{name: "unsupported", fileName: "photo.jpg", input: []byte{0xff}, expectErr: ErrUnsupportedFormat},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Text(context.Background(), tt.fileName, tt.input)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

// Hostile PDFs from testdata/make_pdf_fixtures.py. Each must finish fast and without huge allocations.
func TestHostilePDF(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		file      string
		expected  string
		expectErr error
	}{
		{name: "lying /Count is ignored", file: "pdf-count-1e9.pdf", expected: "Hello brand"},
		{name: "cycle in /Kids", file: "pdf-kids-cycle.pdf", expectErr: errPDFLimit},
		{name: "library panic is recovered", file: "pdf-bad-filter.pdf", expectErr: ErrInvalidDocument},
		{name: "deflate bomb", file: "pdf-stream-bomb.pdf", expectErr: errPDFLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			got, err := Text(ctx, tt.file, readFixture(t, tt.file))
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr)
				require.ErrorIs(t, err, ErrInvalidDocument)
				require.NotErrorIs(t, err, context.DeadlineExceeded)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestRunWithTimeout(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	tests := []struct {
		name      string
		fn        func() (string, error)
		expected  string
		expectErr error
	}{
		{name: "result", fn: func() (string, error) { return "text", nil }, expected: "text"},
		{name: "error", fn: func() (string, error) { return "", errPDFLimit }, expectErr: errPDFLimit},
		{name: "panic", fn: func() (string, error) { panic("library bug") }, expectErr: ErrInvalidDocument},
		{
			name: "timeout",
			fn: func() (string, error) {
				<-block
				return "", errors.New("too late")
			},
			expectErr: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := runWithTimeout(context.Background(), 50*time.Millisecond, tt.fn)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestDocxText(t *testing.T) {
	t.Parallel()

	const body = `<w:p><w:pPr><w:pStyle w:val="1"/></w:pPr><w:r><w:t>Продукты</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t xml:space="preserve">Марафон </w:t></w:r><w:r><w:t>21 день</w:t></w:r>` +
		`<w:r><w:br/><w:t>со скидкой</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/></w:numPr></w:pPr><w:r><w:t>пункт</w:t></w:r></w:p>` +
		`<w:p><w:r><w:delText>удалено</w:delText></w:r></w:p>` +
		`<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>Без styles.xml</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t xml:space="preserve">До рамки </w:t></w:r><w:r><w:txbxContent>` +
		`<w:p><w:r><w:t>В рамке</w:t></w:r></w:p></w:txbxContent></w:r><w:r><w:t>после</w:t></w:r></w:p>`

	// Russian Word names the heading style ID "1"; the English name lives in styles.xml.
	const styles = `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:style w:styleId="1"><w:name w:val="heading 1"/></w:style></w:styles>`

	tests := []struct {
		name     string
		files    map[string]string
		expected string
	}{
		{
			name: "styles, runs, breaks, lists, deleted text",
			files: map[string]string{
				docxDocumentPath: wrapDocument(body),
				docxStylesPath:   styles,
			},
			expected: "# Продукты\n\nМарафон 21 день\nсо скидкой\n\n- пункт\n\n## Без styles.xml\n\nВ рамке\n\nДо рамки после",
		},
		{
			name:     "no styles.xml: style ID 1 is not a heading",
			files:    map[string]string{docxDocumentPath: wrapDocument(body)},
			expected: "Продукты\n\nМарафон 21 день\nсо скидкой\n\n- пункт\n\n## Без styles.xml\n\nВ рамке\n\nДо рамки после",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := docxText(makeZip(t, tt.files))

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestDocxWithoutDocument(t *testing.T) {
	t.Parallel()

	_, err := docxText(makeZip(t, map[string]string{"word/other.xml": "<x/>"}))

	require.ErrorIs(t, err, ErrInvalidDocument)
}

func TestHeadingLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{name: "english name", input: "heading 2", expected: 2},
		{name: "style id", input: "Heading3", expected: 3},
		{name: "title", input: "Title", expected: 1},
		{name: "deep level is capped", input: "heading 9", expected: maxHeadingLevel},
		{name: "normal", input: "Normal", expected: 0},
		{name: "localized id", input: "1", expected: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, headingLevel(tt.input))
		})
	}
}

func TestLayoutText(t *testing.T) {
	t.Parallel()

	const size = 10

	tests := []struct {
		name     string
		input    []glyph
		expected string
	}{
		{
			name: "words by gaps, lines top to bottom, glyph order by x",
			input: []glyph{
				{x: 30, y: 100, width: 5, fontSize: size, text: "b"}, // drawn after its neighbours
				{x: 20, y: 100, width: 5, fontSize: size, text: "a"},
				{x: 35, y: 100, width: 5, fontSize: size, text: "c"},
				{x: 0, y: 100, width: 5, fontSize: size, text: "x"},
				{x: 0, y: 88, width: 5, fontSize: size, text: "y"},
			},
			expected: "x a bc\ny",
		},
		{
			name: "big vertical gap is a paragraph",
			input: []glyph{
				{x: 0, y: 100, width: 5, fontSize: size, text: "a"},
				{x: 0, y: 50, width: 5, fontSize: size, text: "b"},
			},
			expected: "a\n\nb",
		},
		{
			name: "replacement chars dropped, private-use bullets mapped",
			input: []glyph{
				{x: 0, y: 100, width: 5, fontSize: size, text: ""},
				{x: 10, y: 100, width: 5, fontSize: size, text: "�"},
				{x: 20, y: 100, width: 5, fontSize: size, text: "п"},
			},
			expected: "• п",
		},
		{name: "empty", input: nil, expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, layoutText(tt.input))
		})
	}
}

func TestSupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "md", input: "a/voice.md", expected: true},
		{name: "upper docx", input: "BRAND.DOCX", expected: true},
		{name: "pdf", input: "x.pdf", expected: true},
		{name: "doc is not supported", input: "old.doc", expected: false},
		{name: "no extension", input: "README", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, Supported(tt.input))
		})
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)

	return data
}

func wrapDocument(body string) string {
	return `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		body + `</w:body></w:document>`
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)

		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, zw.Close())

	return buf.Bytes()
}
