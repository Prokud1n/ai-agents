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

	style := subtitles.PhraseStyle()
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

// Words of TestRoughCutTranscriptToWords (goconst: the other test of the file has them too).
const (
	langRu    = "ru"
	wFiller   = "эээ"
	wSquat    = "присед"
	wEven     = "ровно"
	wBack     = "спину"
	wHoldCap  = "Держим"
	wDoCap    = "Делаем"
	wHelloCap = "Привет"
	wFriends  = "друзья"
)

var wordRe = regexp.MustCompile(`(?m)^Dialogue: 0,[^,]*,[^,]*,Word,,0,0,0,,\{\\q2\}(.*)$`)

// TestRoughCutTranscriptToWords: in the word layout (the default) the subtitles of a cut come one word at a time from
// the transcript of the edit — a word hidden in the subtitles is not shown, a corrected one is shown as corrected, the
// filler cut by the plan is gone.
func TestRoughCutTranscriptToWords(t *testing.T) {
	t.Parallel()

	source := &transcription.Result{
		Language: langRu,
		Duration: 6,
		Segments: []transcription.Segment{
			{Start: 0.5, End: 2.4, Text: "Привет, друзья! Делаем, эээ, присед."},
			{Start: 3, End: 4.5, Text: "Держим спину ровно."},
		},
		Words: []transcription.Word{
			{Word: wHelloCap, Start: 0.5, End: 0.9}, {Word: wFriends, Start: 0.95, End: 1.3},
			{Word: wDoCap, Start: 1.35, End: 1.7}, {Word: wFiller, Start: 1.75, End: 2.0},
			{Word: wSquat, Start: 2.05, End: 2.4},
			{Word: wHoldCap, Start: 3.0, End: 3.4}, {Word: wBack, Start: 3.45, End: 3.9}, {Word: wEven, Start: 3.95, End: 4.5},
		},
	}
	edit := &roughcut.Edit{
		Hide:  []roughcut.WordRange{{From: 1, To: 1}},
		Words: []roughcut.WordFix{{Index: 5, Text: "Держите"}},
	}

	plan, err := roughcut.BuildPlanEdited(source, source.Duration, roughcut.Settings{
		MaxPause: 0.4, PadBefore: 0.1, PadAfter: 0.15, RetakeSimilarity: 0.7, Fillers: []string{"э"},
	}, edit)
	require.NoError(t, err)

	transcript := roughcut.SubtitleTranscript(source, plan.Keep, edit)

	svc, err := subtitles.NewService(nil, &config.Subtitles{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	// The word layout (the reference reels' style) shows the words one by one.
	style := subtitles.WordsStyle()
	result, err := svc.CreateSubtitles(&subtitles.CreateSubtitlesData{Transcript: &transcript, Style: &style})
	require.NoError(t, err)

	words := make([]string, 0, result.Cues)
	for _, m := range wordRe.FindAllStringSubmatch(string(result.ASS), -1) {
		words = append(words, strings.TrimRight(m[1], "!"))
	}

	assert.Equal(t, []string{"привет", "делаем", wSquat, "держите", wBack, wEven}, words)
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
