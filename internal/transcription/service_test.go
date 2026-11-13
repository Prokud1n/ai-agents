package transcription

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
)

type cutCall struct {
	start    float64
	duration float64
}

type fakeAudio struct {
	duration    float64
	silences    []ffmpeg.Silence
	extractErr  error
	silencesErr error

	mu   sync.Mutex
	cuts []cutCall
}

func (f *fakeAudio) ExtractAudio(_ context.Context, _, dst string) error {
	if f.extractErr != nil {
		return f.extractErr
	}

	return os.WriteFile(dst, []byte("audio"), 0o600)
}

func (f *fakeAudio) CutAudio(_ context.Context, _, dst string, start, duration float64) error {
	f.mu.Lock()
	f.cuts = append(f.cuts, cutCall{start: start, duration: duration})
	f.mu.Unlock()

	return os.WriteFile(dst, []byte("chunk"), 0o600)
}

func (f *fakeAudio) Duration(context.Context, string) (float64, error) {
	return f.duration, nil
}

func (f *fakeAudio) DetectSilences(context.Context, string) ([]ffmpeg.Silence, error) {
	return f.silences, f.silencesErr
}

// fakeTranscriber returns transcripts in call order and records the requests.
type fakeTranscriber struct {
	transcripts []whisper.Transcript
	err         error

	mu       sync.Mutex
	requests []whisper.TranscribeData
}

func (f *fakeTranscriber) Transcribe(_ context.Context, data whisper.TranscribeData) (*whisper.Transcript, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return nil, f.err
	}

	t := f.transcripts[len(f.requests)]
	f.requests = append(f.requests, data)

	return &t, nil
}

func newTestService(audio audioProcessor, tr transcriber) *Service {
	return NewService(audio, tr, &config.Whisper{Language: "ru"}, slog.New(slog.DiscardHandler))
}

func TestServiceTranscribe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		input            TranscribeData
		audio            *fakeAudio
		transcripts      []whisper.Transcript
		expected         *Result
		expectedCuts     []cutCall
		expectedPrompts  []string
		expectedLanguage string
	}{
		{
			name:  "short video is one request without cutting",
			input: TranscribeData{Path: "video.mp4"},
			audio: &fakeAudio{duration: 4.2104},
			transcripts: []whisper.Transcript{{
				Language: "russian", Text: " Привет!",
				Words:    []whisper.Word{{Word: "Привет", Start: 0, End: 0.86}},
				Segments: []whisper.Segment{{Start: 0, End: 1.1, Text: " Привет!"}},
			}},
			expected: &Result{
				Language: "ru", Duration: 4.21, Text: "Привет!",
				Words:    []Word{{Word: "Привет", Start: 0, End: 0.86}},
				Segments: []Segment{{Start: 0, End: 1.1, Text: "Привет!"}},
			},
			expectedPrompts:  []string{""},
			expectedLanguage: "ru",
		},
		{
			name:  "long video is cut in pauses and merged with shifted times",
			input: TranscribeData{Path: "video.mp4", Language: "en"},
			audio: &fakeAudio{duration: 1000, silences: []ffmpeg.Silence{{Start: 499, End: 501}}},
			transcripts: []whisper.Transcript{
				{Text: "One two.", Words: []whisper.Word{{Word: "One", Start: 10, End: 10.5}}, Segments: []whisper.Segment{{Start: 10, End: 11, Text: "One two."}}},
				{Text: "Three.", Words: []whisper.Word{{Word: "Three", Start: 5, End: 5.5}}, Segments: []whisper.Segment{{Start: 5, End: 6, Text: "Three."}}},
			},
			expected: &Result{
				Language: "en", Duration: 1000, Text: "One two. Three.",
				Words:    []Word{{Word: "One", Start: 10, End: 10.5}, {Word: "Three", Start: 505, End: 505.5}},
				Segments: []Segment{{Start: 10, End: 11, Text: "One two."}, {Start: 505, End: 506, Text: "Three."}},
			},
			expectedCuts:     []cutCall{{start: 0, duration: 500}, {start: 500, duration: 500}},
			expectedPrompts:  []string{"", "One two."},
			expectedLanguage: "en",
		},
		{
			name:  "silence detection failure falls back to fixed cuts",
			input: TranscribeData{Path: "video.mp4"},
			audio: &fakeAudio{duration: 700, silencesErr: errors.New("boom")},
			transcripts: []whisper.Transcript{
				{Text: "A.", Words: []whisper.Word{{Word: "A", Start: 1, End: 2}}},
				{Text: "B.", Words: []whisper.Word{{Word: "B", Start: 50, End: 51}}},
			},
			expected: &Result{
				Language: "ru", Duration: 700, Text: "",
				Words:    []Word{{Word: "A", Start: 1, End: 2}, {Word: "B", Start: 648, End: 649}},
				Segments: []Segment{},
			},
			expectedCuts:     []cutCall{{start: 0, duration: 600}, {start: 598, duration: 102}},
			expectedPrompts:  []string{"", "A."},
			expectedLanguage: "ru",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr := &fakeTranscriber{transcripts: tt.transcripts}

			got, err := newTestService(tt.audio, tr).Transcribe(t.Context(), tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
			assert.Equal(t, tt.expectedCuts, tt.audio.cuts)

			prompts := make([]string, 0, len(tr.requests))
			for _, r := range tr.requests {
				prompts = append(prompts, r.Prompt)
				assert.Equal(t, tt.expectedLanguage, r.Language)
				assert.NotEmpty(t, r.Path)
			}

			assert.Equal(t, tt.expectedPrompts, prompts)
		})
	}
}

func TestServiceTranscribeErrors(t *testing.T) {
	t.Parallel()

	errFFmpeg := errors.New("ffmpeg: Stream map '0:a:0' matches no streams")

	tests := []struct {
		name        string
		input       TranscribeData
		audio       *fakeAudio
		trErr       error
		expectedErr error
	}{
		{name: "empty path", input: TranscribeData{}, audio: &fakeAudio{}, expectedErr: ErrEmptyPath},
		{name: "extraction fails", input: TranscribeData{Path: "v.mp4"}, audio: &fakeAudio{extractErr: errFFmpeg}, expectedErr: errFFmpeg},
		{
			name: "no api key", input: TranscribeData{Path: "v.mp4"}, audio: &fakeAudio{duration: 10},
			trErr: whisper.ErrAPIKeyMissing, expectedErr: whisper.ErrAPIKeyMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := newTestService(tt.audio, &fakeTranscriber{err: tt.trErr}).Transcribe(t.Context(), tt.input)

			require.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, got)
		})
	}
}
