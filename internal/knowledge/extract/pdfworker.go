package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

	// The child exits by itself this long after PDFTimeout, in case the parent died without killing it.
	workerSelfDeadline = PDFTimeout + 5*time.Second
	workerMemoryLimit  = "512MiB" // GOMEMLIMIT of the child: the GC works harder near it
	// Address space the child may add to what it has mapped at start (RLIMIT_AS, Linux only):
	// past it allocations fail and the child dies with "out of memory" instead of taking the host's RAM.
	workerAddressSpace = 1 << 30
	workerMaxStack     = 64 << 20 // a runaway recursion dies at 64 MB of stack instead of 1 GB

	maxWorkerOutput   = 16 << 20 // JSON with at most MaxDocumentRunes of text fits easily
	maxWorkerStderr   = 4 << 10  // enough for the first lines of a Go crash report
	workerWaitDelay   = time.Second
	workerOK          = 0
	workerFailed      = 1
	workerSelfTimeout = 2
)

var errOutputTooLarge = errors.New("output limit exceeded")

// workerResult is the child's answer on stdout.
type workerResult struct {
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

// childError is an error the child reported as text: an invalid document, without repeating
// "invalid document", which the child's message already says.
type childError struct {
	msg string
}

func (e *childError) Error() string {
	return e.msg
}

func (*childError) Is(target error) bool {
	return target == ErrInvalidDocument
}

// PDFWorker starts the child process that parses PDFs. It always runs this program's own executable
// with WorkerCommand: no user input reaches the command line.
type PDFWorker struct {
	path    string
	args    []string
	env     []string // the child's whole environment besides GOMEMLIMIT: the parser needs no secrets
	timeout time.Duration
}

// NewPDFWorker runs the current executable with WorkerCommand; the binary must dispatch that command
// to RunPDFWorker before anything else (see internal.RunKB).
func NewPDFWorker() (*PDFWorker, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to find the executable for the pdf parser: %w", err)
	}

	return newPDFWorker(exe, []string{WorkerCommand}, nil, PDFTimeout), nil
}

// newPDFWorker is for tests, which run the test binary as the child.
func newPDFWorker(path string, args, env []string, timeout time.Duration) *PDFWorker {
	return &PDFWorker{path: path, args: args, env: env, timeout: timeout}
}

// Parse runs the child with the PDF on stdin and returns the extracted text.
func (w *PDFWorker) Parse(ctx context.Context, data []byte) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()

	stdout := &limitedBuffer{limit: maxWorkerOutput}
	stderr := &limitedBuffer{limit: maxWorkerStderr, truncate: true}

	// Killed with its process group when runCtx is done. path is this program's own executable and
	// args are fixed (NewPDFWorker): no user input reaches the command line.
	cmd := exec.CommandContext(runCtx, w.path, w.args...) //nolint:gosec // G204: re-runs its own executable, see above
	cmd.Env = append([]string{"GOMEMLIMIT=" + workerMemoryLimit}, w.env...)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = workerWaitDelay
	killProcessGroup(cmd)

	runErr := cmd.Run()

	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("pdf parsing was cancelled: %w", ctx.Err())
	case runCtx.Err() != nil:
		return "", fmt.Errorf("failed to parse pdf in %s: %w: %w", w.timeout, ErrInvalidDocument, runCtx.Err())
	case errors.Is(runErr, errOutputTooLarge) || stdout.exceeded:
		return "", fmt.Errorf("pdf parser output is larger than %d bytes: %w", maxWorkerOutput, ErrInvalidDocument)
	}

	var res workerResult
	if decodeErr := json.Unmarshal(stdout.buf.Bytes(), &res); decodeErr != nil || runErr != nil && res.Error == "" {
		// The child crashed (or was killed by the OS) before answering.
		cause := runErr
		if cause == nil {
			cause = decodeErr
		}

		return "", fmt.Errorf("failed to parse pdf: parser crashed: %s: %w: %w",
			firstLine(stderr.buf.String()), ErrInvalidDocument, cause)
	}

	if res.Error != "" {
		return "", fmt.Errorf("failed to parse pdf: %w", &childError{msg: res.Error})
	}

	return res.Text, nil
}

// RunPDFWorker is the child side: it reads a PDF from in, writes a JSON workerResult to out and
// returns the exit code. It runs in its own process only: it lowers the process's limits and exits
// the process by itself if it outlives the parent's timeout (an orphan must not live forever).
func RunPDFWorker(in io.Reader, out, errOut io.Writer) int {
	time.AfterFunc(workerSelfDeadline, func() {
		_, _ = fmt.Fprintln(errOut, "pdf worker: deadline exceeded")

		os.Exit(workerSelfTimeout)
	})

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
			text, err = "", fmt.Errorf("pdf parser panicked: %v: %w", r, ErrInvalidDocument)
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
