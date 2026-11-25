package editlocal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// LockFile in the output directory is held by the run working in it, so two runs never make versions of one edit
// at the same time.
const LockFile = ".lock"

const (
	lockFields    = 3 // pid, host, time
	lockSeparator = "\n"
)

var (
	// ErrOutLocked: another run works in the output directory.
	ErrOutLocked = errors.New("another run works in the output directory")
	// ErrStaleLock: the lock of a run that is gone (its process is dead or the lock is older than the longest edit).
	ErrStaleLock = errors.New("stale lock of a finished run in the output directory")
)

// lockInfo is what the lock file says about its run.
type lockInfo struct {
	pid   int
	host  string
	since time.Time
}

// acquireLock creates <dir>/.lock exclusively with the PID, the host and the time of this run; release removes it.
// A lock already there fails: ErrStaleLock when its run is surely gone (the same host and a dead PID, or older than
// staleAfter), ErrOutLocked otherwise. A stale lock is not removed here: the owner checks and removes it.
func acquireLock(dir string, staleAfter time.Duration, now time.Time) (func(), error) {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("failed to create %s: %w", dir, err)
	}

	path := filepath.Join(dir, LockFile)
	host, _ := os.Hostname() // best effort: without the host the PID is not checked

	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode)
	if errors.Is(err, os.ErrExist) {
		return nil, lockError(path, host, staleAfter, now)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create %s: %w", path, err)
	}

	content := strings.Join([]string{strconv.Itoa(os.Getpid()), host, now.UTC().Format(time.RFC3339), ""}, lockSeparator)

	_, writeErr := file.WriteString(content)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		_ = os.Remove(path)

		return nil, fmt.Errorf("failed to write %s: %w", path, err)
	}

	return func() { _ = os.Remove(path) }, nil
}

// lockError tells a live lock from a stale one.
func lockError(path, host string, staleAfter time.Duration, now time.Time) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("%s: %w", path, ErrOutLocked)
	}

	info, ok := parseLock(string(raw))
	if !ok {
		return fmt.Errorf("%s is unreadable: %w", path, ErrStaleLock)
	}

	sameHost := host != "" && info.host == host
	if now.Sub(info.since) > staleAfter || (sameHost && !processAlive(info.pid)) {
		return fmt.Errorf("%s (pid %d on %s since %s): %w", path, info.pid, info.host, info.since.Format(time.RFC3339),
			ErrStaleLock)
	}

	return fmt.Errorf("%s (pid %d on %s since %s): %w", path, info.pid, info.host, info.since.Format(time.RFC3339),
		ErrOutLocked)
}

func parseLock(raw string) (lockInfo, bool) {
	fields := strings.Split(strings.TrimSpace(raw), lockSeparator)
	if len(fields) != lockFields {
		return lockInfo{}, false
	}

	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return lockInfo{}, false
	}

	since, err := time.Parse(time.RFC3339, fields[2])
	if err != nil {
		return lockInfo{}, false
	}

	return lockInfo{pid: pid, host: fields[1], since: since}, true
}

// processAlive reports whether a process with the PID runs on this host (signal 0 checks without sending).
func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))

	return err == nil || errors.Is(err, syscall.EPERM)
}
