package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"
)

// PDF parsing runs in a child process. The library can crash in ways no recover or timeout catches
// inside one process (a fatal "stack overflow" from endless recursion on a self-referencing object
// stream), and it cannot be interrupted, so the child is killed when it runs too long and its memory
// is capped by the OS. A crash of the child becomes an ordinary ErrInvalidDocument in the parent.
const (
	// WorkerCommand is the hidden subcommand of a binary that runs RunPDFWorker (see cmd/kb).
	WorkerCommand = "extract-pdf"

	PDFTimeout = 30 * time.Second // the whole document, including the child's start

	workerMemoryLimit = "512MiB" // GOMEMLIMIT of the child: the GC works harder near it
	// Address space the child may add to what it has mapped at start (RLIMIT_AS, Linux only):
	// past it allocations fail and the child dies with "out of memory" instead of taking the host's RAM.
	workerAddressSpace = 1 << 30
	workerMaxStack     = 64 << 20 // a runaway recursion dies at 64 MB of stack instead of 1 GB

	maxWorkerOutput = 16 << 20 // JSON with at most MaxDocumentRunes of text fits easily
	maxWorkerStderr = 4 << 10  // enough for the first lines of a Go crash report
	workerWaitDelay = time.Second
	workerOK        = 0
	workerFailed    = 1
)

var errOutputTooLarge = errors.New("output limit exceeded")

// workerResult is the child's answer on stdout.
type workerResult struct {
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// PDFWorker starts the child process that parses PDFs: Path with Args runs RunPDFWorker.
type PDFWorker struct {
	Path string
	Args []string
	// Env is the child's whole environment besides GOMEMLIMIT: the parser needs no secrets.
	Env     []string
	Timeout time.Duration // PDFTimeout if zero
}

// Parse runs the child with the PDF on stdin and returns the extracted text.
func (w *PDFWorker) Parse(ctx context.Context, data []byte) (string, error) {
	timeout := w.Timeout
	if timeout == 0 {
		timeout = PDFTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := &limitedBuffer{limit: maxWorkerOutput}
	stderr := &limitedBuffer{limit: maxWorkerStderr, truncate: true}

	cmd := exec.CommandContext(ctx, w.Path, w.Args...) // killed (SIGKILL) when ctx is done
	cmd.Env = append([]string{"GOMEMLIMIT=" + workerMemoryLimit}, w.Env...)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = workerWaitDelay

	runErr := cmd.Run()

	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("failed to parse pdf in %s: %w: %w", timeout, ErrInvalidDocument, ctx.Err())
	case errors.Is(runErr, errOutputTooLarge) || stdout.exceeded:
		return "", fmt.Errorf("pdf parser output is larger than %d bytes: %w", maxWorkerOutput, ErrInvalidDocument)
	}

	var res workerResult
	if decodeErr := json.Unmarshal(stdout.buf.Bytes(), &res); decodeErr != nil || runErr != nil && res.Error == "" {
		// The child crashed (or was killed by the OS) before answering.
		return "", fmt.Errorf("failed to parse pdf: parser crashed (%v): %s: %w",
			runErr, firstLine(stderr.buf.String()), ErrInvalidDocument)
	}

	if res.Error != "" {
		return "", fmt.Errorf("failed to parse pdf: %s: %w", res.Error, ErrInvalidDocument)
	}

	return res.Text, nil
}

// RunPDFWorker is the child side: it reads a PDF from in, writes a JSON workerResult to out and
// returns the exit code. It runs in its own process only: it lowers the process's limits.
func RunPDFWorker(in io.Reader, out, errOut io.Writer) int {
	debug.SetMaxStack(workerMaxStack)

	if err := limitAddressSpace(workerAddressSpace); err != nil {
		_, _ = fmt.Fprintf(errOut, "pdf worker: address space is not limited: %v\n", err)
	}

	res := workerResult{}

	data, err := io.ReadAll(io.LimitReader(in, MaxFileBytes+1))

	switch {
	case err != nil:
		res.Error = fmt.Sprintf("failed to read pdf: %v", err)
	case len(data) > MaxFileBytes:
		res.Error = fmt.Sprintf("pdf is larger than %d bytes", MaxFileBytes)
	default:
		res.Text, err = parsePDFRecovered(data)
		if err != nil {
			res.Error = err.Error()
		}
	}

	if err := json.NewEncoder(out).Encode(res); err != nil {
		_, _ = fmt.Fprintf(errOut, "pdf worker: failed to write result: %v\n", err)

		return workerFailed
	}

	return workerOK
}

// parsePDFRecovered turns the library's panics into errors; fatal errors still kill the process.
func parsePDFRecovered(data []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("pdf parser panicked: %v", r)
		}
	}()

	return parsePDF(data)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}

	return s
}

// limitedBuffer keeps at most limit bytes. Past the limit it either fails the write (stdout: the
// child gets an error and the result is rejected) or silently drops the rest (stderr).
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	truncate bool
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if len(p) <= room {
		return b.buf.Write(p)
	}

	b.exceeded = true
	b.buf.Write(p[:max(room, 0)])

	if b.truncate {
		return len(p), nil
	}

	return 0, errOutputTooLarge
}
