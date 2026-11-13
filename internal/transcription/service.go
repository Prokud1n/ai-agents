package transcription

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
)

const (
	tempDirPattern = "transcribe-*"
	audioName      = "audio"
	chunkName      = "chunk-"
)

var ErrEmptyPath = errors.New("input path is empty")

type audioProcessor interface {
	ExtractAudio(ctx context.Context, src, dst string) error
	CutAudio(ctx context.Context, src, dst string, start, duration float64) error
	Duration(ctx context.Context, path string) (float64, error)
	DetectSilences(ctx context.Context, path string) ([]ffmpeg.Silence, error)
}

type transcriber interface {
	Transcribe(ctx context.Context, data whisper.TranscribeData) (*whisper.Transcript, error)
}

// Service turns a video into a transcript with word timestamps: ffmpeg extracts compact speech audio,
// long audio is split into chunks, Whisper transcribes each and the results are merged.
type Service struct {
	audio           audioProcessor
	transcriber     transcriber
	defaultLanguage string
	maxUploadSize   int64
	log             *slog.Logger
}

func NewService(audio audioProcessor, tr transcriber, cfg *config.Whisper, log *slog.Logger) *Service {
	return &Service{
		audio:           audio,
		transcriber:     tr,
		defaultLanguage: cfg.Language,
		maxUploadSize:   whisper.MaxFileSize,
		log:             log,
	}
}

func (s *Service) Transcribe(ctx context.Context, data TranscribeData) (*Result, error) {
	if data.Path == "" {
		return nil, fmt.Errorf("failed to transcribe: %w", ErrEmptyPath)
	}

	language := data.Language
	if language == "" {
		language = s.defaultLanguage
	}

	dir, err := os.MkdirTemp("", tempDirPattern)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			s.log.ErrorContext(ctx, "transcription: failed to remove temp dir", "error", err, "dir", dir)
		}
	}()

	audio := filepath.Join(dir, audioName+ffmpeg.AudioExtension)
	if err := s.audio.ExtractAudio(ctx, data.Path, audio); err != nil {
		return nil, fmt.Errorf("failed to prepare audio: %w", err)
	}

	duration, err := s.audio.Duration(ctx, audio)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare audio: %w", err)
	}

	chunks, err := s.planAudioChunks(ctx, audio, duration)
	if err != nil {
		return nil, err
	}

	s.log.InfoContext(ctx, "transcription: audio prepared", "duration", duration, "chunks", len(chunks))

	parts, err := s.transcribeChunks(ctx, dir, audio, language, chunks)
	if err != nil {
		return nil, err
	}

	result := mergeChunks(parts)
	result.Duration = roundTime(duration)
	result.Language = language

	if result.Language == "" {
		result.Language = parts[0].Transcript.Language
	}

	return &result, nil
}

func (s *Service) planAudioChunks(ctx context.Context, audio string, duration float64) ([]chunk, error) {
	info, err := os.Stat(audio)
	if err != nil {
		return nil, fmt.Errorf("failed to stat audio: %w", err)
	}

	maxChunk := chunkDurationLimit(duration, info.Size(), s.maxUploadSize)
	if duration <= maxChunk {
		return []chunk{{Start: 0, End: duration}}, nil
	}

	silences, err := s.audio.DetectSilences(ctx, audio)
	if err != nil {
		// Without pauses the audio is still cut at fixed points with an overlap.
		s.log.ErrorContext(ctx, "transcription: failed to detect silences", "error", err)
	}

	return planChunks(duration, maxChunk, silences), nil
}

// transcribeChunks runs chunks one by one: each next request gets the tail of the previous text as a prompt.
func (s *Service) transcribeChunks(
	ctx context.Context,
	dir, audio, language string,
	chunks []chunk,
) ([]chunkTranscript, error) {
	parts := make([]chunkTranscript, 0, len(chunks))
	prompt := ""

	for i, c := range chunks {
		path := audio

		if len(chunks) > 1 {
			path = filepath.Join(dir, chunkName+strconv.Itoa(i)+ffmpeg.AudioExtension)
			if err := s.audio.CutAudio(ctx, audio, path, c.Start, c.End-c.Start); err != nil {
				return nil, fmt.Errorf("failed to prepare chunk %d: %w", i, err)
			}
		}

		transcript, err := s.transcriber.Transcribe(ctx, whisper.TranscribeData{Path: path, Language: language, Prompt: prompt})
		if err != nil {
			return nil, fmt.Errorf("failed to transcribe chunk %d of %d: %w", i+1, len(chunks), err)
		}

		s.log.InfoContext(ctx, "transcription: chunk done", "chunk", i+1, "of", len(chunks), "words", len(transcript.Words))

		parts = append(parts, chunkTranscript{Chunk: c, Transcript: *transcript})
		prompt = promptTail(transcript.Text)
	}

	return parts, nil
}
