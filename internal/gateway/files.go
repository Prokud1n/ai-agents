package gateway

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var (
	// ErrNotLocalFile: a relative path. From getFile it means the Bot API server does not run with --local
	// (the public API), and the file is not on this machine.
	ErrNotLocalFile = errors.New("file path is not absolute: run the local bot api server")
	// ErrFileOutsideDir: the path leads outside the allowed directories (through .. or a symlink) or is
	// not a regular file.
	ErrFileOutsideDir = errors.New("file is outside the allowed directories")
	// ErrFileTooLarge: the file is above TELEGRAM_MAX_FILE_SIZE.
	ErrFileTooLarge = errors.New("file is too large")
	// ErrMediaNotDownloaded: Media.Open on a file that was not downloaded.
	ErrMediaNotDownloaded = errors.New("media is not downloaded")
)

// Paths of the Bot API server contain the bot token as a directory name, so no error here contains a path.

// locate finds the allowed directory that contains the absolute path and returns the path relative to it.
func locate(dirs []string, filePath string) (dir, rel string, err error) {
	if !filepath.IsAbs(filePath) {
		return "", "", ErrNotLocalFile
	}

	clean := filepath.Clean(filePath)

	for _, d := range dirs {
		if d == "" {
			continue
		}

		dir = filepath.Clean(d)

		rel, err = filepath.Rel(dir, clean)
		if err == nil && filepath.IsLocal(rel) {
			return dir, rel, nil
		}
	}

	return "", "", ErrFileOutsideDir
}

// statInRoot checks that rel is a regular file inside dir (the file itself is not a symlink; os.Root refuses
// symlinks of the path that lead outside dir) of at most maxSize bytes, and returns its size.
func statInRoot(dir, rel string, maxSize int64) (int64, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, fmt.Errorf("failed to open directory: %w", unwrapPathError(err))
	}
	defer root.Close()

	info, err := root.Lstat(rel)
	if err != nil {
		return 0, fmt.Errorf("failed to stat file: %w", unwrapPathError(err))
	}

	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("not a regular file: %w", ErrFileOutsideDir)
	}

	if info.Size() > maxSize {
		return 0, ErrFileTooLarge
	}

	return info.Size(), nil
}

// openInRoot opens rel through os.Root, so no symlink leads outside dir at the moment of opening, and checks
// the opened file again: there is no check-then-use race. A FIFO or a device is refused before opening, so
// opening never blocks.
func openInRoot(dir, rel string, maxSize int64) (*os.File, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to open directory: %w", unwrapPathError(err))
	}
	defer root.Close()

	info, err := root.Lstat(rel)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", unwrapPathError(err))
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %w", ErrFileOutsideDir)
	}

	file, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", unwrapPathError(err))
	}

	info, err = file.Stat()
	if err != nil {
		_ = file.Close()

		return nil, fmt.Errorf("failed to stat file: %w", unwrapPathError(err))
	}

	if !info.Mode().IsRegular() {
		_ = file.Close()

		return nil, fmt.Errorf("not a regular file: %w", ErrFileOutsideDir)
	}

	if info.Size() > maxSize {
		_ = file.Close()

		return nil, ErrFileTooLarge
	}

	return file, nil
}

// unwrapPathError drops the path from *os.PathError and keeps the operation and the cause.
func unwrapPathError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return fmt.Errorf("%s: %w", pathErr.Op, pathErr.Err)
	}

	return err
}

// mediaFile hides *os.File: its Name and errors contain the path with the token.
type mediaFile struct {
	file *os.File
}

// Read returns io.EOF as is (callers compare it), other errors without the path.
func (m *mediaFile) Read(p []byte) (int, error) {
	n, err := m.file.Read(p)
	if err == nil || errors.Is(err, io.EOF) {
		return n, err
	}

	return n, unwrapPathError(err)
}

func (m *mediaFile) Seek(offset int64, whence int) (int64, error) {
	n, err := m.file.Seek(offset, whence)
	if err != nil {
		return n, unwrapPathError(err)
	}

	return n, nil
}

func (m *mediaFile) Close() error {
	if err := m.file.Close(); err != nil {
		return unwrapPathError(err)
	}

	return nil
}
