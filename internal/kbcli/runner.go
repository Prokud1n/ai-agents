package kbcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/knowledge/chunk"
	"github.com/Prokud1n/ai-agents/internal/knowledge/extract"
)

var ErrIngestFailed = errors.New("some files were not ingested")

type knowledgeService interface {
	Ingest(ctx context.Context, data knowledge.IngestDocumentData) (knowledge.IngestResult, error)
	Search(ctx context.Context, data knowledge.SearchChunksData) ([]knowledge.SearchResult, error)
	Delete(ctx context.Context, source string) error
	Reembed(ctx context.Context) (knowledge.ReembedResult, error)
}

// File is a file to ingest: Path to read it, Source to identify it in the knowledge base.
type File struct {
	Path   string
	Source string
}

// Runner executes kb commands and prints results for a human.
type Runner struct {
	svc knowledgeService
	out io.Writer
	log *slog.Logger
}

func NewRunner(svc *knowledge.Service, log *slog.Logger) *Runner {
	return &Runner{svc: svc, out: os.Stdout, log: log}
}

func (r *Runner) Run(ctx context.Context, cmd *Command) error {
	switch cmd.Action {
	case IngestAction:
		return r.ingest(ctx, cmd.Arg, cmd.Kind)
	case SearchAction:
		return r.search(ctx, cmd)
	case DeleteAction:
		if err := r.svc.Delete(ctx, cmd.Arg); err != nil {
			return fmt.Errorf("failed to delete document: %w", err)
		}

		r.printf("deleted %s\n", cmd.Arg)

		return nil
	case ReembedAction:
		return r.reembed(ctx)
	default:
		return fmt.Errorf("unknown command %q: %w", cmd.Action, ErrUsage)
	}
}

func (r *Runner) search(ctx context.Context, cmd *Command) error {
	results, err := r.svc.Search(ctx, knowledge.SearchChunksData{Query: cmd.Arg, Limit: cmd.Limit, Kinds: cmd.Kinds})
	if err != nil {
		return fmt.Errorf("failed to search: %w", err)
	}

	r.printf("%s\n", knowledge.FormatSearchResults(results))

	return nil
}

func (r *Runner) reembed(ctx context.Context) error {
	res, err := r.svc.Reembed(ctx)

	r.printf("reembedded: %d corrections, %d documents (%d chunks)\n", res.Corrections, res.Documents, res.Chunks)

	if res.StaleChunking > 0 {
		r.printf("%d documents were split by an older chunker: run kb ingest for them again\n", res.StaleChunking)
	}

	if err != nil {
		return fmt.Errorf("failed to reembed: %w", err)
	}

	return nil
}

// ingest loads one file or every supported file under a folder; a failed file does not stop the others.
func (r *Runner) ingest(ctx context.Context, root string, kind knowledge.Kind) error {
	files, err := CollectFiles(root)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("no .md, .txt, .docx or .pdf files in %s: %w", root, ErrUsage)
	}

	failed := 0

	for _, file := range files {
		res, err := r.ingestFile(ctx, file, kind)
		if err != nil {
			failed++

			r.log.ErrorContext(ctx, "kb: failed to ingest file", "error", err, "source", file.Source)
			r.printf("failed    %s: %v\n", file.Source, err)

			continue
		}

		r.printf("%-9s %s (%d chunks)\n", res.Status, file.Source, res.Chunks)
	}

	if failed > 0 {
		return fmt.Errorf("%d of %d files: %w", failed, len(files), ErrIngestFailed)
	}

	return nil
}

func (r *Runner) ingestFile(ctx context.Context, file File, kind knowledge.Kind) (knowledge.IngestResult, error) {
	data, err := ReadFileLimited(file.Path, extract.MaxFileBytes)
	if err != nil {
		return knowledge.IngestResult{}, err
	}

	text, err := extract.Text(ctx, file.Path, data)
	if err != nil {
		return knowledge.IngestResult{}, fmt.Errorf("failed to extract text: %w", err)
	}

	res, err := r.svc.Ingest(ctx, knowledge.IngestDocumentData{
		Source: file.Source,
		Title:  DocumentTitle(text, file.Path),
		Kind:   kind,
		Text:   text,
	})
	if err != nil {
		return knowledge.IngestResult{}, fmt.Errorf("failed to ingest document: %w", err)
	}

	return res, nil
}

func (r *Runner) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.out, format, args...)
}

// ReadFileLimited reads a file of at most limit bytes; the size is checked before reading,
// and the read itself is limited in case the file grows meanwhile.
func ReadFileLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	if info.Size() > limit {
		return nil, fmt.Errorf("%d bytes, at most %d: %w", info.Size(), limit, extract.ErrTooLarge)
	}

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	if int64(len(data)) > limit {
		return nil, fmt.Errorf("more than %d bytes: %w", limit, extract.ErrTooLarge)
	}

	return data, nil
}

// CollectFiles returns the file itself or supported files under the folder (hidden ones skipped).
// Source is the slash-separated path relative to root (for a single file, its name), so it does not
// depend on the current directory or on how root was spelled.
func CollectFiles(root string) ([]File, error) {
	root = filepath.Clean(root)

	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", root, err)
	}

	if !info.IsDir() {
		if !extract.Supported(root) {
			return nil, fmt.Errorf("%s: %w", root, extract.ErrUnsupportedFormat)
		}

		return []File{{Path: root, Source: filepath.Base(root)}}, nil
	}

	var files []File

	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if p != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if d.IsDir() || !extract.Supported(p) {
			return nil
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("failed to resolve source of %s: %w", p, err)
		}

		files = append(files, File{Path: p, Source: filepath.ToSlash(rel)})

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk %s: %w", root, err)
	}

	return files, nil
}

// DocumentTitle is the first level-1 heading, or the file name without extension.
func DocumentTitle(text, file string) string {
	if title := chunk.Title(text); title != "" {
		return title
	}

	base := filepath.Base(file)

	return strings.TrimSuffix(base, filepath.Ext(base))
}
