package main

import (
	"bytes"
	"flag"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

const (
	testVideo      = "video.mp4"
	inFlag         = "-in"
	similarityFlag = "-similarity"
	transcriptFlag = "-transcript"
	planFlag       = "-plan"
	testTranscript = "t.json"
	acceptFlag     = "-accept"
)

func TestParseFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       []string
		expected    options
		expectedErr error
	}{
		{
			name:     "input only",
			input:    []string{inFlag, testVideo},
			expected: options{in: testVideo, out: defaultOutDir, similarity: -1},
		},
		{
			name: "all flags",
			input: []string{
				inFlag, testVideo, transcriptFlag, testTranscript, planFlag, "plan.json", "-out", "cut", "-lang", "en",
				"-plan-only", "-max-pause", "300ms", similarityFlag, "0.8", "-fillers", "э,ну",
			},
			expected: options{
				in: testVideo, transcript: testTranscript, plan: "plan.json", out: "cut", lang: "en", planOnly: true,
				maxPause: 300 * time.Millisecond, similarity: 0.8, fillers: "э,ну",
			},
		},
		{name: "help", input: []string{"-h"}, expectedErr: flag.ErrHelp},
		{name: "missing input", input: []string{"-plan-only"}, expectedErr: errNoInput},
		{
			name:     "accepted candidates",
			input:    []string{inFlag, testVideo, transcriptFlag, testTranscript, acceptFlag, "0, 2"},
			expected: options{in: testVideo, transcript: testTranscript, out: defaultOutDir, similarity: -1, accept: []int{0, 2}},
		},
		{name: "bad candidate ID", input: []string{inFlag, testVideo, acceptFlag, "x"}, expectedErr: errBadAccept},
		{
			name:        "accept with an edited plan",
			input:       []string{inFlag, testVideo, transcriptFlag, testTranscript, planFlag, "p.json", acceptFlag, "1"},
			expectedErr: errAcceptWithPlan,
		},
		{name: "plan without transcript", input: []string{inFlag, testVideo, planFlag, "p.json"}, expectedErr: errPlanNoTranscript},
		{name: "negative pause", input: []string{inFlag, testVideo, "-max-pause", "-1s"}, expectedErr: errNegativePause},
		{name: "similarity above one", input: []string{inFlag, testVideo, similarityFlag, "1.5"}, expectedErr: errInvalidSimilarity},
		{name: "negative similarity", input: []string{inFlag, testVideo, similarityFlag, "-0.5"}, expectedErr: errInvalidSimilarity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer

			got, err := parseFlags(tt.input, &output)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)
				assert.Contains(t, output.String(), "usage: roughcut")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestApplyOverrides(t *testing.T) {
	t.Parallel()

	defaults := roughcut.Settings{MaxPause: 0.4, PadBefore: 0.08, PadAfter: 0.12, RetakeSimilarity: 0.7, Fillers: []string{"э"}}

	tests := []struct {
		name     string
		input    options
		expected roughcut.Settings
	}{
		{name: "no overrides", input: options{similarity: -1}, expected: defaults},
		{
			name:  "all overrides",
			input: options{maxPause: 250 * time.Millisecond, similarity: 0.9, fillers: " э, ммм ,,"},
			expected: roughcut.Settings{
				MaxPause: 0.25, PadBefore: 0.08, PadAfter: 0.12, RetakeSimilarity: 0.9, Fillers: []string{"э", "ммм"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, applyOverrides(defaults, &tt.input))
		})
	}
}

func TestPrintSummary(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := printSummary(&out, &roughcut.Result{
		Log:   "Черновая склейка: clip.mp4\n",
		Files: roughcut.Files{Plan: "cut/plan.json", Log: "cut/decisions.log", Transcript: "cut/transcript.json"},
	})

	require.NoError(t, err)
	assert.Equal(t,
		"Черновая склейка: clip.mp4\n\nФайлы:\n- cut/plan.json\n- cut/decisions.log\n- cut/transcript.json\n",
		out.String())
}
