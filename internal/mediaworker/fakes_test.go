package mediaworker

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	testSourceKey    = "incoming/-1001234567890/42/uniq.mov"
	testResultPrefix = "edits"
	testLanguage     = "ru"
)

// localStore is S3 on the local disk: sources are files, results are kept in memory with their content type.
type localStore struct {
	mu      sync.Mutex
	sources map[string]string // key -> path
	results map[string]storedResult
	order   []string
	putErr  error
}

type storedResult struct {
	data        []byte
	contentType string
}

func newLocalStore(sources map[string]string) *localStore {
	return &localStore{sources: sources, results: map[string]storedResult{}}
}

func (s *localStore) Download(_ context.Context, key, dir string, maxSize int64) (*media.LocalFile, error) {
	path, ok := s.sources[key]
	if !ok {
		return nil, media.ErrFileNotFound
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if int64(len(data)) > maxSize {
		return nil, media.ErrFileTooLarge
	}

	file, err := os.CreateTemp(dir, "source-*.mov")
	if err != nil {
		return nil, err
	}

	if _, err := file.Write(data); err != nil {
		_ = file.Close()

		return nil, err
	}

	if err := file.Close(); err != nil {
		return nil, err
	}

	return &media.LocalFile{Path: file.Name(), Size: int64(len(data))}, nil
}

func (s *localStore) PutResult(_ context.Context, key string, r io.ReadSeeker, contentType string) error {
	if s.putErr != nil {
		return s.putErr
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.results[key] = storedResult{data: data, contentType: contentType}
	s.order = append(s.order, key)

	return nil
}

// Read returns a stored result (the transcript of an earlier job).
func (s *localStore) Read(_ context.Context, key string, maxSize int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, ok := s.results[key]
	if !ok {
		return nil, media.ErrFileNotFound
	}

	if int64(len(stored.data)) > maxSize {
		return nil, media.ErrFileTooLarge
	}

	return stored.data, nil
}

func (*localStore) ResultKey(sourceKey, name, ext string) (string, error) {
	info, err := media.ParseKey(sourceKey)
	if err != nil {
		return "", err
	}

	return media.ResultKey(testResultPrefix, info, name, ext)
}

type fakeProber struct {
	info ffmpeg.MediaInfo
	err  error
}

func (p *fakeProber) Probe(context.Context, string) (ffmpeg.MediaInfo, error) {
	return p.info, p.err
}

type fakeKeys struct{ err error }

func (k fakeKeys) CheckAPIKey() error { return k.err }

type fakeTranscriber struct {
	result *transcription.Result
	err    error
	calls  int
}

func (f *fakeTranscriber) Transcribe(context.Context, transcription.TranscribeData) (*transcription.Result, error) {
	f.calls++

	return f.result, f.err
}

type fakeCutter struct {
	result *roughcut.Result
	err    error
	got    *roughcut.CutData
}

func (f *fakeCutter) Cut(_ context.Context, data *roughcut.CutData) (*roughcut.Result, error) {
	f.got = data
	if f.err != nil {
		return nil, f.err
	}

	if err := os.MkdirAll(data.OutDir, 0o700); err != nil {
		return nil, err
	}

	result := *f.result
	result.Files.Plan = filepath.Join(data.OutDir, roughcut.PlanFile)
	result.Files.Log = filepath.Join(data.OutDir, roughcut.LogFile)

	for _, file := range []string{result.Files.Plan, result.Files.Log} {
		if err := os.WriteFile(file, []byte(filepath.Base(file)), 0o600); err != nil {
			return nil, err
		}
	}

	return &result, nil
}

type fakeBurner struct {
	result *subtitles.BurnResult
	err    error
	got    *subtitles.BurnSubtitlesData
}

func (f *fakeBurner) BurnSubtitles(_ context.Context, data *subtitles.BurnSubtitlesData) (*subtitles.BurnResult, error) {
	f.got = data
	if f.err != nil {
		return nil, f.err
	}

	if err := os.WriteFile(data.OutputPath, []byte("reel"), 0o600); err != nil {
		return nil, err
	}

	return f.result, nil
}
