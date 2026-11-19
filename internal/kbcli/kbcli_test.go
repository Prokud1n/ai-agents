package kbcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/knowledge/extract"
)

const (
	ingestCmd   = string(IngestAction)
	searchCmd   = string(SearchAction)
	deleteCmd   = string(DeleteAction)
	kindFlag    = "-kind"
	brandName   = string(knowledge.BrandKind)
	testQuery   = "питание"
	voiceSource = "brand/voice.md"
	testSource  = "a.md"
	voiceFile   = "voice.md"
	notesFile   = "notes.txt"
	docxFile    = "products.docx"
	subDocxFile = "sub/" + docxFile
)

// testWorkerEnv makes the test binary act as the PDF child process, like `kb extract-pdf`.
const testWorkerEnv = "KBCLI_TEST_PDF_WORKER"

func TestMain(m *testing.M) {
	if os.Getenv(testWorkerEnv) == "1" {
		os.Exit(extract.RunPDFWorker(os.Stdin, os.Stdout, os.Stderr))
	}

	os.Exit(m.Run())
}

func newTestRunner(t *testing.T, svc knowledgeService, root string, out io.Writer) *Runner {
	t.Helper()

	exe, err := os.Executable()
	require.NoError(t, err)

	worker := &extract.PDFWorker{Path: exe, Env: []string{testWorkerEnv + "=1", "GORACE=atexit_sleep_ms=0"}}

	return &Runner{
		svc:       svc,
		extractor: extract.NewExtractor(worker),
		root:      root,
		out:       out,
		log:       slog.New(slog.DiscardHandler),
	}
}

func TestParseCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     []string
		expected  Command
		expectErr bool
	}{
		{
			name:     "ingest with default kind",
			input:    []string{ingestCmd, brandName},
			expected: Command{Action: IngestAction, Arg: brandName},
		},
		{
			name:     "ingest with kind after path",
			input:    []string{ingestCmd, "./brand", kindFlag, brandName},
			expected: Command{Action: IngestAction, Arg: "./brand", Kind: knowledge.BrandKind},
		},
		{
			name:     "ingest with kind before path",
			input:    []string{ingestCmd, "-kind=product", docxFile},
			expected: Command{Action: IngestAction, Arg: docxFile, Kind: knowledge.ProductKind},
		},
		{
			name:     "search with limit and kinds",
			input:    []string{searchCmd, "как мы говорим о питании", "-limit", "3", kindFlag, "brand, post"},
			expected: Command{Action: SearchAction, Arg: "как мы говорим о питании", Limit: 3, Kinds: []knowledge.Kind{knowledge.BrandKind, knowledge.PostKind}},
		},
		{
			name:     "search with default limit",
			input:    []string{searchCmd, testQuery},
			expected: Command{Action: SearchAction, Arg: testQuery, Limit: knowledge.DefaultLimit},
		},
		{
			name:     "delete by source",
			input:    []string{deleteCmd, voiceSource},
			expected: Command{Action: DeleteAction, Arg: voiceSource},
		},
		{name: "no command", input: nil, expectErr: true},
		{name: "unknown command", input: []string{"list"}, expectErr: true},
		{name: "missing argument", input: []string{deleteCmd}, expectErr: true},
		{name: "two arguments", input: []string{searchCmd, "a", "b"}, expectErr: true},
		{name: "empty argument", input: []string{searchCmd, " "}, expectErr: true},
		{name: "unknown kind", input: []string{ingestCmd, testSource, kindFlag, "recipe"}, expectErr: true},
		{name: "unknown search kind", input: []string{searchCmd, "a", kindFlag, "brand,recipe"}, expectErr: true},
		{name: "flag of another command", input: []string{deleteCmd, testSource, kindFlag, brandName}, expectErr: true},
		{name: "bad limit", input: []string{searchCmd, "a", "-limit", "many"}, expectErr: true},
		{
			name:     "ingest with force",
			input:    []string{ingestCmd, "-force", "a", kindFlag, brandName},
			expected: Command{Action: IngestAction, Arg: "a", Kind: knowledge.BrandKind, Force: true},
		},
		{name: "reembed", input: []string{string(ReembedAction)}, expected: Command{Action: ReembedAction}},
		{name: "reembed with argument", input: []string{string(ReembedAction), "x"}, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseCommand(tt.input)
			if tt.expectErr {
				require.ErrorIs(t, err, ErrUsage)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestCollectFiles(t *testing.T) {
	t.Parallel()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	for _, name := range []string{voiceFile, notesFile, "photo.jpg", ".hidden.md", subDocxFile, ".git/x.md"} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	}

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, voiceFile), []byte("x"), 0o600))

	file := func(rel string) File { return File{Path: filepath.Join(dir, rel), Source: rel} }
	all := []File{file(notesFile), file(subDocxFile), file(voiceFile)}

	tests := []struct {
		name      string
		root      string
		input     string
		expected  []File
		expectErr error
	}{
		{name: "root itself: supported, not hidden, recursive", root: dir, input: dir, expected: all},
		{name: "root spelled differently", root: dir + "/./sub/..", input: dir, expected: all},
		{name: "subfolder: sources stay relative to root", root: dir, input: filepath.Join(dir, "sub"), expected: []File{file(subDocxFile)}},
		{name: "single file in a subfolder", root: dir, input: filepath.Join(dir, subDocxFile), expected: []File{file(subDocxFile)}},
		{name: "single file", root: dir, input: filepath.Join(dir, voiceFile), expected: []File{file(voiceFile)}},
		{name: "file outside root", root: dir, input: filepath.Join(outside, voiceFile), expectErr: ErrOutsideRoot},
		{name: "folder outside root", root: filepath.Join(dir, "sub"), input: dir, expectErr: ErrOutsideRoot},
		{name: "unsupported file", root: dir, input: filepath.Join(dir, "photo.jpg"), expectErr: extract.ErrUnsupportedFormat},
		{name: "missing path", root: dir, input: filepath.Join(dir, "nope"), expectErr: os.ErrNotExist},
		{name: "missing root", root: filepath.Join(dir, "nope"), input: dir, expectErr: os.ErrNotExist},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CollectFiles(tt.root, tt.input)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestReadFileLimited(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(path, []byte("12345"), 0o600))

	tests := []struct {
		name      string
		limit     int64
		expectErr error
	}{
		{name: "fits", limit: 5},
		{name: "too large", limit: 4, expectErr: extract.ErrTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := ReadFileLimited(path, tt.limit)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, "12345", string(data))
		})
	}
}

func TestDocumentTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		file     string
		expected string
	}{
		{name: "first h1", text: "# Голос бренда\n\nтекст", file: voiceSource, expected: "Голос бренда"},
		{name: "file name", text: "просто текст", file: "brand/Продукты 2026.docx", expected: "Продукты 2026"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, DocumentTitle(tt.text, tt.file))
		})
	}
}

type fakeService struct {
	ingested []knowledge.IngestDocumentData
	failOn   string
}

func (f *fakeService) Ingest(_ context.Context, data knowledge.IngestDocumentData) (knowledge.IngestResult, error) {
	if data.Source == f.failOn {
		return knowledge.IngestResult{}, errors.New("boom")
	}

	f.ingested = append(f.ingested, data)

	return knowledge.IngestResult{DocumentID: 1, Status: knowledge.CreatedIngestStatus, Chunks: 1}, nil
}

func (*fakeService) Search(context.Context, knowledge.SearchChunksData) ([]knowledge.SearchResult, error) {
	return []knowledge.SearchResult{{Source: testSource, Title: "A", Kind: knowledge.BrandKind, Content: "текст", Score: 1}}, nil
}

func (*fakeService) Delete(context.Context, string) error {
	return nil
}

func (*fakeService) Reembed(context.Context) (knowledge.ReembedResult, error) {
	return knowledge.ReembedResult{Corrections: 2, Documents: 1, Chunks: 3, StaleChunking: 1}, nil
}

func TestRunnerIngest(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, voiceFile), []byte("# Голос\n\nна ты"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.md"), []byte("x"), 0o600))

	svc := &fakeService{failOn: "broken.md"}
	out := &bytes.Buffer{}
	runner := newTestRunner(t, svc, dir, out)

	err := runner.Run(context.Background(), &Command{Action: IngestAction, Arg: dir, Kind: knowledge.BrandKind, Force: true})

	require.ErrorIs(t, err, ErrIngestFailed)
	require.Len(t, svc.ingested, 1)
	assert.Equal(t, knowledge.IngestDocumentData{
		Source: voiceFile, Title: "Голос", Kind: knowledge.BrandKind, Text: "# Голос\n\nна ты", Force: true,
	}, svc.ingested[0])
	assert.Contains(t, out.String(), "created   "+voiceFile+" (1 chunks)")
	assert.Contains(t, out.String(), "failed    broken.md")
}

// A PDF that crashes its parser process fails as a validation error of that file; the kb process
// (here: the test) survives and goes on with the next file.
func TestRunnerIngestHostilePDF(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fixture, err := filepath.Abs(filepath.Join("..", "knowledge", "extract", "testdata", "pdf-objstm-self.pdf"))
	require.NoError(t, err)
	require.NoError(t, os.Symlink(fixture, filepath.Join(dir, "a-hostile.pdf")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, voiceFile), []byte("# Голос\n\nна ты"), 0o600))

	svc := &fakeService{}
	runner := newTestRunner(t, svc, dir, &bytes.Buffer{})

	file := File{Path: filepath.Join(dir, "a-hostile.pdf"), Source: "a-hostile.pdf"}
	_, err = runner.ingestFile(context.Background(), file, &Command{Action: IngestAction})
	require.ErrorIs(t, err, knowledge.ErrValidation)
	require.ErrorIs(t, err, extract.ErrInvalidDocument)

	err = runner.Run(context.Background(), &Command{Action: IngestAction, Arg: dir})

	require.ErrorIs(t, err, ErrIngestFailed)
	require.Len(t, svc.ingested, 1)
	assert.Equal(t, voiceFile, svc.ingested[0].Source)
}

func TestRunnerReembed(t *testing.T) {
	t.Parallel()

	out := &bytes.Buffer{}
	runner := newTestRunner(t, &fakeService{}, t.TempDir(), out)

	require.NoError(t, runner.Run(context.Background(), &Command{Action: ReembedAction}))

	assert.Equal(t, "reembedded: 2 corrections, 1 documents (3 chunks)\n"+
		"1 documents were split by an older chunker: run kb ingest for them again\n", out.String())
}

func TestRunnerSearchAndDelete(t *testing.T) {
	t.Parallel()

	out := &bytes.Buffer{}
	runner := newTestRunner(t, &fakeService{}, t.TempDir(), out)

	require.NoError(t, runner.Run(context.Background(), &Command{Action: SearchAction, Arg: testQuery}))
	require.NoError(t, runner.Run(context.Background(), &Command{Action: DeleteAction, Arg: testSource}))

	assert.Equal(t, "[1] a.md — «A» (brand, score 1.00)\nтекст\ndeleted a.md\n", out.String())
}
