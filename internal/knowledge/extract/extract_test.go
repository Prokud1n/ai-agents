package extract

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testWorkerEnv makes the test binary act as the PDF child process (the os/exec tests' pattern):
// the parent tests start os.Executable() with it. Other values simulate a misbehaving child.
const (
	testWorkerEnv   = "EXTRACT_TEST_WORKER"
	workerModePDF   = "pdf"
	workerModeHang  = "hang"
	workerModeCrash = "crash"
	workerModeSpam  = "spam"
	workerModeAlloc = "alloc"

	hugeAllocation = 2 << 30 // above workerAddressSpace
)

func TestMain(m *testing.M) {
	switch os.Getenv(testWorkerEnv) {
	case "":
		os.Exit(m.Run())
	case workerModePDF:
		os.Exit(RunPDFWorker(os.Stdin, os.Stdout, os.Stderr))
	case workerModeHang:
		time.Sleep(time.Hour)
	case workerModeCrash:
		_, _ = os.Stderr.WriteString("fatal error: stack overflow\n\ngoroutine 1 [running]:\n")
		os.Exit(2)
	case workerModeAlloc:
		if err := limitAddressSpace(workerAddressSpace); err != nil {
			os.Exit(3)
		}

		buf := make([]byte, hugeAllocation)
		for i := 0; i < len(buf); i += os.Getpagesize() {
			buf[i] = 1
		}

		_, _ = os.Stdout.WriteString(`{"text":"allocated"}`)
		os.Exit(0)
	case workerModeSpam:
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), maxWorkerOutput+1))
		os.Exit(0)
	}
}

func testWorker(t *testing.T, mode string) *PDFWorker {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	// Under -race the child would sleep 1 s at exit (GORACE atexit_sleep_ms defaults to 1000).
	return newPDFWorker(exe, nil, []string{testWorkerEnv + "=" + mode, "GORACE=atexit_sleep_ms=0"}, PDFTimeout)
}

func testExtractor(t *testing.T) *Extractor {
	t.Helper()

	return NewExtractor(testWorker(t, workerModePDF))
}

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

			got, err := testExtractor(t).Text(context.Background(), tt.fileName, tt.input)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

// Hostile PDFs from testdata/make_pdf_fixtures.py, parsed in a real child process. Each must fail fast
// with an ordinary error; the test process itself (the parent) must survive.
func TestHostilePDF(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		file      string
		expected  string
		expectMsg string // part of the error; empty means success
	}{
		{name: "lying /Count is ignored", file: "pdf-count-1e9.pdf", expected: "Hello brand"},
		{name: "cycle in /Kids", file: "pdf-kids-cycle.pdf", expectMsg: "exceeds extraction limits"},
		{name: "library panic is recovered", file: "pdf-bad-filter.pdf", expectMsg: "panicked"},
		{name: "deflate bomb", file: "pdf-stream-bomb.pdf", expectMsg: "bytes per page"},
		// The library recurses forever resolving an object stream that contains itself: a fatal
		// stack overflow, which only process isolation contains.
		{name: "self-referencing object stream", file: "pdf-objstm-self.pdf", expectMsg: "parser crashed"},
		// One 4 MB stream of numbers referenced 20 times: stopped by the per-page budget.
		{name: "repeated references to one big stream", file: "pdf-refs20.pdf", expectMsg: "bytes per page"},
		// 50,000 Tf operators with a 30,000-entry ToUnicode map: refused by the CMap limit (and the
		// decoder is cached per font name, so Tf no longer re-parses the map).
		{name: "font switched 50,000 times", file: "pdf-tf-reparse.pdf", expectMsg: "ToUnicode map has more than"},
		// The same 50,000 Tf with a 4,000-entry map pass the CMap limit: fast only because the decoder
		// is cached per font name (without the cache this file takes more than 40 s).
		{name: "font switched 50,000 times, allowed map", file: "pdf-tf-switch.pdf", expected: "A"},
		// 100,000 codes missing from a 30,000-entry map: each one scans the whole map.
		{name: "codes scanning a huge cmap", file: "pdf-decode-scan.pdf", expectMsg: "ToUnicode map has more than"},
		// 400,000 nested arrays: handled by the library's parser without deep recursion.
		{name: "deeply nested arrays", file: "pdf-deep-arrays.pdf", expectMsg: "no text layer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			start := time.Now()
			got, err := testExtractor(t).Text(context.Background(), tt.file, readFixture(t, tt.file))

			assert.Less(t, time.Since(start), 10*time.Second)

			if tt.expectMsg != "" {
				// The child reports its error as text: the parent sees ErrInvalidDocument.
				require.ErrorIs(t, err, ErrInvalidDocument)
				require.NotErrorIs(t, err, context.DeadlineExceeded)
				assert.Contains(t, err.Error(), tt.expectMsg)
				assert.Equal(t, 1, strings.Count(err.Error(), ErrInvalidDocument.Error()), err.Error())

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestPDFWorkerFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mode      string
		timeout   time.Duration
		expectErr error
		expectMsg string
	}{
		{name: "hung child is killed", mode: workerModeHang, timeout: 300 * time.Millisecond, expectErr: context.DeadlineExceeded},
		{name: "crashed child", mode: workerModeCrash, expectErr: ErrInvalidDocument, expectMsg: "fatal error: stack overflow"},
		{name: "oversized output", mode: workerModeSpam, expectErr: ErrInvalidDocument, expectMsg: "output is larger"},
	}

	if runtime.GOOS == "linux" {
		// RLIMIT_AS: the child cannot take 2 GB; it dies instead of eating the host's memory.
		tests = append(tests, struct {
			name      string
			mode      string
			timeout   time.Duration
			expectErr error
			expectMsg string
		}{name: "memory cap", mode: workerModeAlloc, expectErr: ErrInvalidDocument, expectMsg: "parser crashed"})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			worker := testWorker(t, tt.mode)
			if tt.timeout > 0 {
				worker.timeout = tt.timeout
			}

			start := time.Now()
			_, err := worker.Parse(context.Background(), []byte("%PDF-1.4"))

			require.ErrorIs(t, err, tt.expectErr)
			require.ErrorIs(t, err, ErrInvalidDocument)
			assert.Contains(t, err.Error(), tt.expectMsg)
			assert.Less(t, time.Since(start), 10*time.Second)
		})
	}
}

// Cancelling the caller's context is reported as a cancellation, not as the parser's timeout.
func TestPDFWorkerParentCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	_, err := testWorker(t, workerModeHang).Parse(ctx, []byte("%PDF-1.4"))

	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrInvalidDocument)
	assert.Contains(t, err.Error(), "cancelled")
	assert.Less(t, time.Since(start), 10*time.Second)
}

func TestLimitedBuffer(t *testing.T) {
	t.Parallel()

	const kept = "abcd"

	tests := []struct {
		name      string
		truncate  bool
		writes    []string
		expected  string
		expectErr bool
	}{
		{name: "fits", writes: []string{"ab", "cd"}, expected: kept},
		{name: "overflow fails", writes: []string{"abc", "def"}, expected: kept, expectErr: true},
		{name: "overflow truncates", truncate: true, writes: []string{"abc", "def"}, expected: kept},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := &limitedBuffer{limit: 4, truncate: tt.truncate}

			var err error
			for _, w := range tt.writes {
				if _, werr := b.Write([]byte(w)); werr != nil {
					err = werr
				}
			}

			assert.Equal(t, tt.expected, b.buf.String())
			assert.Equal(t, tt.expectErr, err != nil)
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
