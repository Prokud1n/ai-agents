package transcription

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
)

const (
	langRU     = "ru"
	langEN     = "en"
	videoPath  = "video.mp4"
	textOneTwo = "One two."
	textThree  = "Three."
)

type cutCall struct {
	start    float64
	duration float64
}

type fakeAudio struct {
	extracted   bool
	loudness    *ffmpeg.Loudness
	duration    float64
	silences    []ffmpeg.Silence
	extractErr  error
	silencesErr error

	mu   sync.Mutex
	cuts []cutCall
}

func (f *fakeAudio) ExtractAudioMeasured(_ context.Context, _, dst string) (*ffmpeg.Loudness, error) {
	f.mu.Lock()
	f.extracted = true
	f.mu.Unlock()

	if f.extractErr != nil {
		return nil, f.extractErr
	}

	return f.loudness, os.WriteFile(dst, []byte("audio"), 0o600)
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

func (f *fakeAudio) DetectSilences(context.Context, string, ffmpeg.SilenceOptions) ([]ffmpeg.Silence, error) {
	return f.silences, f.silencesErr
}

// fakeTranscriber returns transcripts in call order and records the requests.
type fakeTranscriber struct {
	transcripts []whisper.Transcript
	err         error
	keyErr      error

	mu       sync.Mutex
	requests []whisper.TranscribeData
}

func (f *fakeTranscriber) CheckAPIKey() error {
	return f.keyErr
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
	return newTestServiceWithLanguage(audio, tr, langRU)
}

func newTestServiceWithLanguage(audio audioProcessor, tr transcriber, defaultLanguage string) *Service {
	return NewService(
		audio, tr,
		&config.Whisper{Language: defaultLanguage},
		&config.Transcription{SilenceNoiseDB: -30, SilenceMinDuration: 500 * time.Millisecond},
		slog.New(slog.DiscardHandler),
	)
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
			input: TranscribeData{Path: videoPath},
			audio: &fakeAudio{duration: 4.2104, loudness: &ffmpeg.Loudness{Integrated: -27.5, Threshold: -38, Range: 6.2, TruePeak: -9.1}},
			transcripts: []whisper.Transcript{{
				Language: langRussian, Text: segmentHello,
				Words:    []whisper.Word{{Word: wordHello, Start: 0, End: 0.86}},
				Segments: []whisper.Segment{{Start: 0, End: 1.1, Text: segmentHello}},
			}},
			// The loudness measured while the audio was extracted goes into the transcript for the render.
			expected: &Result{
				Language: langRU, Duration: 4.21, Text: textHello,
				Words:    []Word{{Word: wordHello, Start: 0, End: 0.86}},
				Segments: []Segment{{Start: 0, End: 1.1, Text: textHello}},
				Loudness: &ffmpeg.Loudness{Integrated: -27.5, Threshold: -38, Range: 6.2, TruePeak: -9.1},
			},
			expectedPrompts:  []string{""},
			expectedLanguage: langRU,
		},
		{
			name:  "long video is cut in pauses and merged with shifted times",
			input: TranscribeData{Path: videoPath, Language: langEN},
			audio: &fakeAudio{duration: 1000, silences: []ffmpeg.Silence{{Start: 499, End: 501}}},
			transcripts: []whisper.Transcript{
				{Text: textOneTwo, Words: []whisper.Word{{Word: "One", Start: 10, End: 10.5}}, Segments: []whisper.Segment{{Start: 10, End: 11, Text: textOneTwo}}},
				{Text: textThree, Words: []whisper.Word{{Word: "Three", Start: 5, End: 5.5}}, Segments: []whisper.Segment{{Start: 5, End: 6, Text: textThree}}},
			},
			expected: &Result{
				Language: langEN, Duration: 1000, Text: "One two. Three.",
				Words:    []Word{{Word: "One", Start: 10, End: 10.5}, {Word: "Three", Start: 505, End: 505.5}},
				Segments: []Segment{{Start: 10, End: 11, Text: textOneTwo}, {Start: 505, End: 506, Text: textThree}},
			},
			expectedCuts:     []cutCall{{start: 0, duration: 500}, {start: 500, duration: 500}},
			expectedPrompts:  []string{"", textOneTwo},
			expectedLanguage: langEN,
		},
		{
			name:  "silence detection failure falls back to fixed cuts",
			input: TranscribeData{Path: videoPath},
			audio: &fakeAudio{duration: 700, silencesErr: errors.New("boom")},
			transcripts: []whisper.Transcript{
				{Text: "A.", Words: []whisper.Word{{Word: "A", Start: 1, End: 2}}},
				{Text: "B.", Words: []whisper.Word{{Word: "B", Start: 50, End: 51}}},
			},
			expected: &Result{
				Language: langRU, Duration: 700, Text: "",
				Words:    []Word{{Word: "A", Start: 1, End: 2}, {Word: "B", Start: 648, End: 649}},
				Segments: []Segment{},
			},
			expectedCuts:     []cutCall{{start: 0, duration: 600}, {start: 598, duration: 102}},
			expectedPrompts:  []string{"", "A."},
			expectedLanguage: langRU,
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
		name            string
		input           TranscribeData
		audio           *fakeAudio
		transcriber     *fakeTranscriber
		expectedErr     error
		expectExtracted bool
	}{
		{
			name: "no api key fails before audio is prepared", input: TranscribeData{Path: videoPath},
			audio: &fakeAudio{duration: 10}, transcriber: &fakeTranscriber{keyErr: whisper.ErrAPIKeyMissing},
			expectedErr: whisper.ErrAPIKeyMissing,
		},
		{
			name: "empty path", input: TranscribeData{},
			audio: &fakeAudio{}, transcriber: &fakeTranscriber{}, expectedErr: ErrEmptyPath,
		},
		{
			name: "unsupported language", input: TranscribeData{Path: videoPath, Language: langRussian},
			audio: &fakeAudio{}, transcriber: &fakeTranscriber{}, expectedErr: ErrUnsupportedLanguage,
		},
		{
			name: "extraction fails", input: TranscribeData{Path: videoPath},
			audio: &fakeAudio{extractErr: errFFmpeg}, transcriber: &fakeTranscriber{},
			expectedErr: errFFmpeg, expectExtracted: true,
		},
		{
			name: "transcription fails", input: TranscribeData{Path: videoPath},
			audio: &fakeAudio{duration: 10}, transcriber: &fakeTranscriber{err: errFFmpeg},
			expectedErr: errFFmpeg, expectExtracted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := newTestService(tt.audio, tt.transcriber).Transcribe(t.Context(), tt.input)

			require.ErrorIs(t, err, tt.expectedErr)
			assert.Nil(t, got)
			assert.Equal(t, tt.expectExtracted, tt.audio.extracted)
		})
	}
}

// Whisper "hears" text in silence and music; such segments are dropped with their words.
func TestServiceTranscribeSilence(t *testing.T) {
	t.Parallel()

	hallucination := whisper.Transcript{
		Language: langRussian,
		Text:     " Продолжение следует...",
		Words: []whisper.Word{
			{Word: "Продолжение", Start: 0.5, End: 1.4},
			{Word: "следует", Start: 1.4, End: 2.2},
		},
		Segments: []whisper.Segment{
			{Start: 0, End: 3, Text: " Продолжение следует...", NoSpeechProb: 0.92, AvgLogprob: -1.31, CompressionRatio: 0.8},
		},
	}

	tests := []struct {
		name            string
		defaultLanguage string
		input           TranscribeData
		expected        *Result
	}{
		{
			name:            "only silence gives an empty transcript",
			defaultLanguage: langRU,
			input:           TranscribeData{Path: videoPath},
			expected:        &Result{Language: langRU, Duration: 3, Text: "", Words: []Word{}, Segments: []Segment{}},
		},
		{
			name:            "requested language is normalized",
			defaultLanguage: langRU,
			input:           TranscribeData{Path: videoPath, Language: " EN "},
			expected:        &Result{Language: langEN, Duration: 3, Text: "", Words: []Word{}, Segments: []Segment{}},
		},
		{
			name:     "detected language is an ISO code",
			input:    TranscribeData{Path: videoPath},
			expected: &Result{Language: langRU, Duration: 3, Text: "", Words: []Word{}, Segments: []Segment{}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr := &fakeTranscriber{transcripts: []whisper.Transcript{hallucination}}

			got, err := newTestServiceWithLanguage(&fakeAudio{duration: 3}, tr, tt.defaultLanguage).
				Transcribe(t.Context(), tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}
