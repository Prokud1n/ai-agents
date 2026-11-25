package subtitles_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

var dialogueRe = regexp.MustCompile(`(?m)^Dialogue: 0,(\d+):(\d{2}):(\d{2}\.\d{2}),(\d+):(\d{2}):(\d{2}\.\d{2}),Main,,0,0,0,,(.*)$`)

// TestRoughCutTranscriptToSubtitles is the hand-off of the montage pipeline: the transcript.json the rough cut
// writes (T3.1 format on the new timeline, punctuation of the segments kept around cut words) goes into
// subtitles, and its punctuation and times reach the subtitles. Synthetic data, no ffmpeg.
func TestRoughCutTranscriptToSubtitles(t *testing.T) {
	t.Parallel()

	// Like Whisper: words without punctuation, segments with it; a filler and a long pause to cut.
	source := &transcription.Result{
		Language: "ru",
		Duration: 9,
		Segments: []transcription.Segment{
			{Start: 0.5, End: 4.1, Text: "Привет, друзья! Сегодня, эээ, делаем присед."},
			{Start: 7, End: 8.5, Text: "Держим спину ровно."},
		},
		Words: []transcription.Word{
			{Word: "Привет", Start: 0.5, End: 0.9}, {Word: "друзья", Start: 0.95, End: 1.4},
			{Word: "Сегодня", Start: 1.8, End: 2.3}, {Word: "эээ", Start: 2.4, End: 3.0},
			{Word: "делаем", Start: 3.1, End: 3.5}, {Word: "присед", Start: 3.55, End: 4.1},
			{Word: "Держим", Start: 7.0, End: 7.4}, {Word: "спину", Start: 7.45, End: 7.9}, {Word: "ровно", Start: 7.95, End: 8.5},
		},
	}

	plan := roughcut.BuildPlan(source, source.Duration, roughcut.Settings{
		MaxPause: 0.4, PadBefore: 0.1, PadAfter: 0.15, RetakeSimilarity: 0.7, Fillers: []string{"э"},
	})
	remapped := roughcut.RemapTranscript(source, plan.Keep)

	// Through JSON, as the file transcript.json is passed between the commands.
	file, err := json.Marshal(remapped)
	require.NoError(t, err)

	var transcript transcription.Result
	require.NoError(t, json.Unmarshal(file, &transcript))

	svc, err := subtitles.NewService(nil, &config.Subtitles{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	style := svc.Style()
	style.Accent.MaxPerCue = 0 // plain text: the test is about words, punctuation and times

	result, err := svc.CreateSubtitles(&subtitles.CreateSubtitlesData{Transcript: &transcript, Style: &style})
	require.NoError(t, err)

	matches := dialogueRe.FindAllStringSubmatch(string(result.ASS), -1)
	require.Len(t, matches, result.Cues)

	texts := make([]string, 0, len(matches))
	end := 0.0

	for _, m := range matches {
		texts = append(texts, m[7])
		end = assTime(t, m[4], m[5], m[6])
	}

	assert.Equal(t, []string{`Привет, друзья!`, `Сегодня,\Nделаем присед`, `Держим спину ровно`}, texts,
		"punctuation of the cut transcript reaches the subtitles; the filler is gone")
	assert.InDelta(t, plan.Duration, end, 0.2, "subtitles follow the new timeline, not the source")
	assert.Less(t, end, source.Duration-2, "the cut pause and filler shortened the timeline")
}

func assTime(t *testing.T, h, m, s string) float64 {
	t.Helper()

	hours, err := strconv.Atoi(h)
	require.NoError(t, err)

	minutes, err := strconv.Atoi(m)
	require.NoError(t, err)

	seconds, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	require.NoError(t, err)

	return float64(hours*3600+minutes*60) + seconds
}
