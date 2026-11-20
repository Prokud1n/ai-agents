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
	secretFile  = "secret.md"
	secretValue = "sk-secret"
	aliasFile   = "alias.md"
	plainFile   = "a.txt"
	voiceText   = "# Голос\n\nна ты"
	notesFile   = "notes.txt"
	docxFile    = "products.docx"
	subDocxFile = "sub/" + docxFile
)

// The runner uses the real extract.NewPDFWorker: it re-runs this test binary with `extract-pdf`,
// which TestMain dispatches to the PDF child, exactly like `kb extract-pdf` in internal.RunKB.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == extract.WorkerCommand {
		os.Exit(extract.RunPDFWorker(os.Stdin, os.Stdout, os.Stderr))
	}

	os.Exit(m.Run())
}

func newTestRunner(t *testing.T, svc knowledgeService, root string, out io.Writer) *Runner {
	t.Helper()

	worker, err := extract.NewPDFWorker()
	require.NoError(t, err)

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
		{
			name:     "search query starting with a dash after --",
			input:    []string{searchCmd, "-limit", "3", "--", "-5 кг"},
			expected: Command{Action: SearchAction, Arg: "-5 кг", Limit: 3},
		},
		{
			name:     "flags after -- are part of the arguments",
			input:    []string{deleteCmd, "--", "-limit"},
			expected: Command{Action: DeleteAction, Arg: "-limit"},
		},
		{name: "two arguments after --", input: []string{searchCmd, "--", "-5", "-limit"}, expectErr: true},
		{name: "query starting with a dash without --", input: []string{searchCmd, "-5 кг"}, expectErr: true},
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

func TestRelativeToRoot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	tests := []struct {
		name      string
		root      string
		input     string
		expected  string
		expectErr error
	}{
		{name: "root itself", root: dir, input: dir, expected: "."},
		{name: "nested", root: dir, input: filepath.Join(dir, brandName, voiceFile), expected: brandName + "/" + voiceFile},
		{name: "spelled with dots", root: dir + "/./brand/..", input: dir + "/brand/../" + voiceFile, expected: voiceFile},
		{name: "outside", root: filepath.Join(dir, brandName), input: filepath.Join(dir, voiceFile), expectErr: ErrOutsideRoot},
		{name: "parent of root", root: filepath.Join(dir, brandName), input: dir, expectErr: ErrOutsideRoot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := RelativeToRoot(tt.root, tt.input)
			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestCollectFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{voiceFile, notesFile, "photo.jpg", ".hidden.md", subDocxFile, ".git/x.md"} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	}

	// Symlinks inside the root: to a file outside it, to a folder outside it and to a file inside it.
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, secretFile), []byte("OPENAI_API_KEY="+secretValue), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(outside, secretFile), filepath.Join(dir, "leak.md")))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "leakdir")))
	// os.Root follows only relative links that stay inside it; an absolute link counts as escaping.
	require.NoError(t, os.Symlink(voiceFile, filepath.Join(dir, aliasFile)))

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	all := []File{{Source: notesFile}, {Source: subDocxFile}, {Source: voiceFile}}

	tests := []struct {
		name      string
		input     string
		expected  []File
		expectErr error
	}{
		{name: "root: supported, not hidden, recursive, no symlinks", input: ".", expected: all},
		{name: "subfolder: sources stay relative to root", input: "sub", expected: []File{{Source: subDocxFile}}},
		{name: "single file", input: voiceFile, expected: []File{{Source: voiceFile}}},
		{name: "symlink inside root, named explicitly", input: aliasFile, expected: []File{{Source: aliasFile}}},
		{name: "symlink leading outside, named explicitly", input: "leak.md", expectErr: os.ErrPermission},
		{name: "symlinked folder leading outside", input: "leakdir", expectErr: os.ErrPermission},
		{name: "unsupported file", input: "photo.jpg", expectErr: extract.ErrUnsupportedFormat},
		{name: "missing path", input: "nope", expectErr: os.ErrNotExist},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CollectFiles(root, tt.input)
			if tt.expectErr != nil {
				require.Error(t, err)

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
	require.NoError(t, os.WriteFile(filepath.Join(dir, plainFile), []byte("12345"), 0o600))

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(secretValue), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "leak.txt")))

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	tests := []struct {
		name      string
		file      string
		limit     int64
		expectErr bool
	}{
		{name: "fits", file: plainFile, limit: 5},
		{name: "too large", file: plainFile, limit: 4, expectErr: true},
		{name: "symlink leading outside is not read", file: "leak.txt", limit: 100, expectErr: true},
		{name: "path leading outside is not read", file: "../" + filepath.Base(outside) + "/secret.txt", limit: 100, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := ReadFileLimited(root, tt.file, tt.limit)
			if tt.expectErr {
				require.Error(t, err)
				assert.Empty(t, data)

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
	require.NoError(t, os.WriteFile(filepath.Join(dir, voiceFile), []byte(voiceText), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.md"), []byte("x"), 0o600))

	svc := &fakeService{failOn: "broken.md"}
	out := &bytes.Buffer{}
	runner := newTestRunner(t, svc, dir, out)

	err := runner.Run(context.Background(), &Command{Action: IngestAction, Arg: dir, Kind: knowledge.BrandKind, Force: true})

	require.ErrorIs(t, err, ErrIngestFailed)
	require.Len(t, svc.ingested, 1)
	assert.Equal(t, knowledge.IngestDocumentData{
		Source: voiceFile, Title: "Голос", Kind: knowledge.BrandKind, Text: voiceText, Force: true,
	}, svc.ingested[0])
	assert.Contains(t, out.String(), "created   "+voiceFile+" (1 chunks)")
	assert.Contains(t, out.String(), "failed    broken.md")
}

// A PDF that crashes its parser process fails as a validation error of that file; the kb process
// (here: the test) survives and goes on with the next file. testdata/hostile holds real files.
func TestRunnerIngestHostilePDF(t *testing.T) {
	t.Parallel()

	const root = "testdata/hostile"

	svc := &fakeService{}
	runner := newTestRunner(t, svc, root, &bytes.Buffer{})

	err := runner.Run(context.Background(), &Command{Action: IngestAction, Arg: filepath.Join(root, "a-hostile.pdf")})
	require.ErrorIs(t, err, ErrIngestFailed)

	err = runner.Run(context.Background(), &Command{Action: IngestAction, Arg: root})

	require.ErrorIs(t, err, ErrIngestFailed)
	require.Len(t, svc.ingested, 1)
	assert.Equal(t, voiceFile, svc.ingested[0].Source)
}

// A symlink inside KB_ROOT that leads outside is never read: skipped when its folder is ingested,
// an error when named explicitly. Its target's content never reaches the service (and so OpenAI).
func TestRunnerIngestSymlinkOutsideRoot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, secretFile), []byte("OPENAI_API_KEY="+secretValue), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(outside, secretFile), filepath.Join(dir, notesFile+".md")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, voiceFile), []byte(voiceText), 0o600))

	svc := &fakeService{}
	out := &bytes.Buffer{}
	runner := newTestRunner(t, svc, dir, out)

	require.NoError(t, runner.Run(context.Background(), &Command{Action: IngestAction, Arg: dir}))

	err := runner.Run(context.Background(), &Command{Action: IngestAction, Arg: filepath.Join(dir, notesFile+".md")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path escapes")

	require.Len(t, svc.ingested, 1)
	assert.Equal(t, voiceFile, svc.ingested[0].Source)
	assert.NotContains(t, svc.ingested[0].Text, secretValue)
	assert.NotContains(t, out.String(), secretValue)
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
