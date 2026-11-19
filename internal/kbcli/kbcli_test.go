package kbcli

import (
	"bytes"
	"context"
	"errors"
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
			expected: Command{Action: IngestAction, Arg: brandName, Kind: knowledge.OtherKind},
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

	dir := t.TempDir()
	for _, name := range []string{voiceFile, notesFile, "photo.jpg", ".hidden.md", subDocxFile, ".git/x.md"} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	}

	file := func(rel string) File { return File{Path: filepath.Join(dir, rel), Source: rel} }

	tests := []struct {
		name      string
		input     string
		expected  []File
		expectErr bool
	}{
		{
			name:     "folder: supported, not hidden, recursive, sources relative to root",
			input:    dir,
			expected: []File{file(notesFile), file(subDocxFile), file(voiceFile)},
		},
		{
			name:     "root spelled differently gives the same sources",
			input:    dir + "/./sub/..",
			expected: []File{file(notesFile), file(subDocxFile), file(voiceFile)},
		},
		{
			name:     "subfolder as root",
			input:    filepath.Join(dir, "sub"),
			expected: []File{{Path: filepath.Join(dir, subDocxFile), Source: docxFile}},
		},
		{name: "single file: source is its name", input: filepath.Join(dir, voiceFile), expected: []File{file(voiceFile)}},
		{name: "unsupported file", input: filepath.Join(dir, "photo.jpg"), expectErr: true},
		{name: "missing path", input: filepath.Join(dir, "nope"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CollectFiles(tt.input)
			if tt.expectErr {
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
	runner := &Runner{svc: svc, out: out, log: slog.New(slog.DiscardHandler)}

	err := runner.Run(context.Background(), &Command{Action: IngestAction, Arg: dir, Kind: knowledge.BrandKind})

	require.ErrorIs(t, err, ErrIngestFailed)
	require.Len(t, svc.ingested, 1)
	assert.Equal(t, knowledge.IngestDocumentData{Source: voiceFile, Title: "Голос", Kind: knowledge.BrandKind, Text: "# Голос\n\nна ты"}, svc.ingested[0])
	assert.Contains(t, out.String(), "created   "+voiceFile+" (1 chunks)")
	assert.Contains(t, out.String(), "failed    broken.md")
}

func TestRunnerReembed(t *testing.T) {
	t.Parallel()

	out := &bytes.Buffer{}
	runner := &Runner{svc: &fakeService{}, out: out, log: slog.New(slog.DiscardHandler)}

	require.NoError(t, runner.Run(context.Background(), &Command{Action: ReembedAction}))

	assert.Equal(t, "reembedded: 2 corrections, 1 documents (3 chunks)\n"+
		"1 documents were split by an older chunker: run kb ingest for them again\n", out.String())
}

func TestRunnerSearchAndDelete(t *testing.T) {
	t.Parallel()

	out := &bytes.Buffer{}
	runner := &Runner{svc: &fakeService{}, out: out, log: slog.New(slog.DiscardHandler)}

	require.NoError(t, runner.Run(context.Background(), &Command{Action: SearchAction, Arg: testQuery}))
	require.NoError(t, runner.Run(context.Background(), &Command{Action: DeleteAction, Arg: testSource}))

	assert.Equal(t, "[1] a.md — «A» (brand, score 1.00)\nтекст\ndeleted a.md\n", out.String())
}
