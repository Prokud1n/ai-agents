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

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/knowledge/chunk"
	"github.com/Prokud1n/ai-agents/internal/knowledge/extract"
)

var (
	ErrIngestFailed = errors.New("some files were not ingested")
	ErrOutsideRoot  = errors.New("path is outside KB_ROOT")
)

type knowledgeService interface {
	Ingest(ctx context.Context, data knowledge.IngestDocumentData) (knowledge.IngestResult, error)
	Search(ctx context.Context, data knowledge.SearchChunksData) ([]knowledge.SearchResult, error)
	Delete(ctx context.Context, source string) error
	Reembed(ctx context.Context) (knowledge.ReembedResult, error)
}

// File is a file to ingest. Source identifies it in the knowledge base and is also its slash-separated
// path inside KB_ROOT, which is the only way it is read (through os.Root).
type File struct {
	Source string
}

type textExtractor interface {
	Text(ctx context.Context, name string, data []byte) (string, error)
}

// Runner executes kb commands and prints results for a human.
type Runner struct {
	svc       knowledgeService
	extractor textExtractor
	root      string // KB_ROOT
	out       io.Writer
	log       *slog.Logger
}

// NewRunner parses PDFs in child processes: this executable re-run with the hidden extract-pdf
// command (see internal.RunKB), with an empty environment, so the parser sees no secrets.
func NewRunner(svc *knowledge.Service, cfg *config.KnowledgeBase, log *slog.Logger) (*Runner, error) {
	worker, err := extract.NewPDFWorker()
	if err != nil {
		return nil, fmt.Errorf("failed to set up the pdf parser: %w", err)
	}

	return &Runner{
		svc:       svc,
		extractor: extract.NewExtractor(worker),
		root:      cfg.Root,
		out:       os.Stdout,
		log:       log,
	}, nil
}

func (r *Runner) Run(ctx context.Context, cmd *Command) error {
	switch cmd.Action {
	case IngestAction:
		return r.ingest(ctx, cmd)
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
func (r *Runner) ingest(ctx context.Context, cmd *Command) error {
	rel, err := RelativeToRoot(r.root, cmd.Arg)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(r.root)
	if err != nil {
		return fmt.Errorf("failed to open KB_ROOT: %w", err)
	}
	defer root.Close()

	files, err := CollectFiles(root, rel)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("no .md, .txt, .docx or .pdf files in %s: %w", cmd.Arg, ErrUsage)
	}

	failed := 0

	for _, file := range files {
		res, err := r.ingestFile(ctx, root, file, cmd)
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

// ingestFile reads and extracts one file. A file that cannot be read as its format (including a PDF
// whose parser process crashed) is a validation error of that file only.
func (r *Runner) ingestFile(
	ctx context.Context,
	root *os.Root,
	file File,
	cmd *Command,
) (knowledge.IngestResult, error) {
	data, err := ReadFileLimited(root, file.Source, extract.MaxFileBytes)
	if err != nil {
		return knowledge.IngestResult{}, fmt.Errorf("%w: %w", knowledge.ErrValidation, err)
	}

	text, err := r.extractor.Text(ctx, file.Source, data)
	if err != nil {
		return knowledge.IngestResult{}, fmt.Errorf("failed to extract text: %w: %w", knowledge.ErrValidation, err)
	}

	res, err := r.svc.Ingest(ctx, knowledge.IngestDocumentData{
		Source: file.Source,
		Title:  DocumentTitle(text, file.Source),
		Kind:   cmd.Kind,
		Text:   text,
		Force:  cmd.Force,
	})
	if err != nil {
		return knowledge.IngestResult{}, fmt.Errorf("failed to ingest document: %w", err)
	}

	return res, nil
}

func (r *Runner) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.out, format, args...)
}

// ReadFileLimited reads a file inside root of at most limit bytes. os.Root refuses paths and symlinks
// that lead outside root, at the moment of opening (no check-then-use race). The size is checked
// before reading, and the read itself is limited in case the file grows meanwhile.
func ReadFileLimited(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := root.Open(filepath.FromSlash(name))
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

// RelativeToRoot turns the path given to ingest (relative to the current directory or absolute) into
// a slash-separated path inside root ("." for root itself); a path outside root is ErrOutsideRoot.
// It is lexical: symlinks are dealt with when files are opened through os.Root.
func RelativeToRoot(root, path string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("failed to resolve KB_ROOT: %w", err)
	}

	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve %s: %w", path, err)
	}

	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s (KB_ROOT is %s): %w", path, rootAbs, ErrOutsideRoot)
	}

	return filepath.ToSlash(rel), nil
}

// CollectFiles returns the file rel itself or the supported files under the folder rel, both inside root.
// Hidden entries and symlinks met while walking a folder are skipped: a symlink could lead outside
// KB_ROOT or ingest one file under two sources. A file or folder named explicitly may be a symlink
// inside root; one leading outside fails when opened.
func CollectFiles(root *os.Root, rel string) ([]File, error) {
	fsys := root.FS()

	info, err := fs.Stat(fsys, rel)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", rel, err)
	}

	if !info.IsDir() {
		if !extract.Supported(rel) {
			return nil, fmt.Errorf("%s: %w", rel, extract.ErrUnsupportedFormat)
		}

		return []File{{Source: rel}}, nil
	}

	var files []File

	err = fs.WalkDir(fsys, rel, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if p == rel {
			return nil
		}

		if strings.HasPrefix(d.Name(), ".") || d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}

			return nil
		}

		if !d.IsDir() && extract.Supported(p) {
			files = append(files, File{Source: p})
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk %s: %w", rel, err)
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
