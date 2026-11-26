package editlocal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/media"
)

const (
	// localFilePrefix starts the file name of local sources in S3: local-<8 random hex>.
	localFilePrefix = "local-"
	localNameBytes  = 4
	// newSourceAttempts: task IDs tried for a key that is not taken (a reset Redis counter gives IDs used before).
	newSourceAttempts = 10
)

// ErrNoFreeKey: every key tried for the new source is taken in S3.
var ErrNoFreeKey = errors.New("no free key for the local source")

// newSource is the file of a new local source: the next task ID and a random name, whose key is not in S3 yet. After
// a reset of Redis the counter gives IDs used before; the random name and the check keep the old sources and their
// edits as they are.
func (r *Runner) newSource(ctx context.Context, name string) (*media.File, error) {
	for range newSourceAttempts {
		taskID, err := r.sequence.NextTaskID(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get a local task id: %w", err)
		}

		random := make([]byte, localNameBytes)
		if _, err := rand.Read(random); err != nil {
			return nil, fmt.Errorf("failed to make the source name: %w", err)
		}

		file := &media.File{
			ChatID: LocalChatID, MessageID: taskID, FileUniqueID: localFilePrefix + hex.EncodeToString(random), FileName: name,
		}

		key, err := r.store.Key(file)
		if err != nil {
			return nil, fmt.Errorf("failed to make the source key: %w", err)
		}

		taken, err := r.exists(ctx, key)
		if err != nil {
			return nil, err
		}

		if !taken {
			return file, nil
		}

		r.log.WarnContext(ctx, "editlocal: the source key is taken, trying the next task id", "key", key)
	}

	return nil, ErrNoFreeKey
}

// checkSource fails with ErrSourceMissing when the source of the edit is gone from S3.
func (r *Runner) checkSource(ctx context.Context, key string) error {
	found, err := r.exists(ctx, key)
	if err != nil {
		return err
	}

	if !found {
		return fmt.Errorf("%s: %w", key, ErrSourceMissing)
	}

	return nil
}

// exists reports whether S3 has an object under the key (a HEAD request).
func (r *Runner) exists(ctx context.Context, key string) (bool, error) {
	found, err := r.store.Exists(ctx, key)
	if err != nil {
		return false, fmt.Errorf("failed to check %s in S3: %w", key, err)
	}

	return found, nil
}

// download copies the reel, the plan and the decisions of the version into dir.
func (r *Runner) download(ctx context.Context, sourceKey string, version int, dir string) error {
	files, err := editjob.Files(r.store, sourceKey, version)
	if err != nil {
		return fmt.Errorf("failed to make the keys of the result: %w", err)
	}

	if err := mkdir(dir); err != nil {
		return err
	}

	for _, f := range []struct{ key, name string }{
		{key: files.Reel, name: ReelFile},
		{key: files.Plan, name: PlanFile},
		{key: files.Decisions, name: DecisionsFile},
	} {
		local, err := r.store.Download(ctx, f.key, dir, r.settings.MaxFileSize)
		if err != nil {
			return fmt.Errorf("failed to download %s: %w", f.name, err)
		}

		if err := os.Rename(local.Path, filepath.Join(dir, f.name)); err != nil {
			_ = local.Remove()

			return fmt.Errorf("failed to save %s: %w", f.name, err)
		}
	}

	return nil
}

// openSource opens the local source read-only after the checks the bot makes: a regular file of a video type, not
// empty and not above maxSize.
func openSource(path string, maxSize int64) (*os.File, error) {
	name := filepath.Base(path)
	if media.Extension("", name) == media.UnknownExtension {
		return nil, fmt.Errorf("%s: not a video file (mp4, mov, webm, mkv, avi, 3gp, mpeg): %w", name, ErrInvalidSource)
	}

	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("failed to open the source: %w", err)
	}

	info, err := file.Stat()

	switch {
	case err != nil:
		err = fmt.Errorf("failed to read the source: %w", err)
	case !info.Mode().IsRegular():
		err = fmt.Errorf("%s is not a regular file: %w", name, ErrInvalidSource)
	case info.Size() == 0:
		err = fmt.Errorf("%s is empty: %w", name, ErrInvalidSource)
	case info.Size() > maxSize:
		err = fmt.Errorf("%s is %d bytes, above the limit of %d (TELEGRAM_MAX_FILE_SIZE): %w", name, info.Size(), maxSize,
			ErrInvalidSource)
	}

	if err != nil {
		_ = file.Close()

		return nil, err
	}

	return file, nil
}

func mkdir(dir string) error {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}

	return nil
}

func writeFile(dir, name, content string) error {
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), fileMode); err != nil {
		return fmt.Errorf("failed to write %s: %w", name, err)
	}

	return nil
}
