package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/s3"
)

const tempFilePattern = "media-*"

type objectStorage interface {
	PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
}

// Store copies incoming files to S3 and gives them back as local files.
type Store struct {
	storage       objectStorage
	prefix        string
	uploadTimeout time.Duration
	log           *slog.Logger
}

// NewStore fails on an invalid MEDIA_KEY_PREFIX, so a typo stops the start.
func NewStore(storage objectStorage, cfg *config.Media, log *slog.Logger) (*Store, error) {
	if err := validatePrefix(cfg.KeyPrefix); err != nil {
		return nil, err
	}

	if cfg.UploadTimeout <= 0 {
		return nil, fmt.Errorf("MEDIA_UPLOAD_TIMEOUT must be positive: %w", ErrInvalidConfig)
	}

	return &Store{storage: storage, prefix: cfg.KeyPrefix, uploadTimeout: cfg.UploadTimeout, log: log}, nil
}

// Save copies the file to S3 under its key (see Key) and returns the key. The size is taken from the file
// itself (seek to the end), so a wrong size reported by Telegram cannot cut or stall the upload. The same
// message saved again replaces the same object.
func (s *Store) Save(ctx context.Context, f *File, r io.ReadSeeker) (string, error) {
	key, err := Key(s.prefix, f)
	if err != nil {
		return "", err
	}

	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return "", fmt.Errorf("failed to measure media file: %w", err)
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("failed to rewind media file: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, s.uploadTimeout)
	defer cancel()

	ext := Extension(f.MIMEType, f.FileName)
	if err := s.storage.PutObject(ctx, key, r, size, ContentType(f.MIMEType, ext)); err != nil {
		return "", fmt.Errorf("failed to save media file: %w", err)
	}

	s.log.InfoContext(ctx, "media: file saved", "key", key, "size", size)

	return key, nil
}

// LocalFile is a downloaded copy of a stored file in the temporary directory; Remove deletes it.
type LocalFile struct {
	// Path ends with the extension of the key.
	Path string
	// Size is the number of bytes written.
	Size int64
}

// Remove deletes the local copy; a copy already removed is not an error.
func (f *LocalFile) Remove() error {
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove local media file: %w", err)
	}

	return nil
}

// Download copies a stored file to a new temporary file in dir (empty: the system temporary directory) and returns
// it; the caller removes it. At most maxSize bytes are written: a larger object is ErrFileTooLarge and leaves no
// file behind, so a wrong object cannot fill the disk. Only keys of this store are accepted (ErrInvalidKey);
// a missing object is ErrFileNotFound.
func (s *Store) Download(ctx context.Context, key, dir string, maxSize int64) (*LocalFile, error) {
	info, err := s.parseOwnKey(key)
	if err != nil {
		return nil, err
	}

	obj, err := s.storage.GetObject(ctx, key)
	if err != nil {
		return nil, notFound(err)
	}
	defer obj.Close()

	file, err := os.CreateTemp(dir, tempFilePattern+info.Extension)
	if err != nil {
		return nil, fmt.Errorf("failed to create local media file: %w", err)
	}

	local := &LocalFile{Path: file.Name()}

	size, copyErr := io.Copy(file, io.LimitReader(obj, maxSize+1))
	if copyErr == nil && size > maxSize {
		copyErr = fmt.Errorf("object is above %d bytes: %w", maxSize, ErrFileTooLarge)
	}

	closeErr := file.Close()

	if err := errors.Join(copyErr, closeErr); err != nil {
		if removeErr := local.Remove(); removeErr != nil {
			s.log.ErrorContext(ctx, "media: failed to remove a partial download", "error", removeErr, "key", key)
		}

		return nil, fmt.Errorf("failed to download media file: %w", err)
	}

	local.Size = size

	return local, nil
}

// parseOwnKey parses the key and checks that it is under this store's prefix.
func (s *Store) parseOwnKey(key string) (KeyInfo, error) {
	info, err := ParseKey(key)
	if err != nil {
		return KeyInfo{}, err
	}

	if info.Prefix != s.prefix {
		return KeyInfo{}, fmt.Errorf("key outside %s: %w", s.prefix, ErrInvalidKey)
	}

	return info, nil
}

func notFound(err error) error {
	if errors.Is(err, s3.ErrObjectNotFound) {
		return fmt.Errorf("failed to download media file: %w", ErrFileNotFound)
	}

	return fmt.Errorf("failed to download media file: %w", err)
}
