package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
)

func TestSnapToFrames(t *testing.T) {
	t.Parallel()

	plan := Plan{
		SourceDuration: 10,
		Duration:       3.16,
		Keep:           []Interval{{Start: 0.92, End: 2.42}, {Start: 2.43, End: 3.01}, {Start: 5.001, End: 5.012}, {Start: 9, End: 9.99}},
		Cuts:           []Cut{{Start: 2.42, End: 2.43, Reason: PauseCutReason}, {Start: 3.01, End: 5.001, Reason: FillerCutReason, Text: testFiller}},
		Candidates:     []Candidate{{ID: 0, Cut: Interval{Start: 2.43, End: 5.001}}},
	}

	tests := []struct {
		name         string
		input        float64
		expectedKeep []Interval
		expectedCuts []Cut
		expectedFPS  float64
		expectedDur  float64
		expectedCand Interval
	}{
		{
			name:  "edges move to 25 fps frames, empty intervals vanish, touching ones merge",
			input: 25,
			expectedKeep: []Interval{
				{Start: 0.92, End: 3}, {Start: 9, End: 10},
			},
			expectedCuts: []Cut{{Start: 3, End: 5, Reason: FillerCutReason, Text: testFiller}},
			expectedFPS:  25,
			expectedDur:  3.08,
			expectedCand: Interval{Start: 2.44, End: 5},
		},
		{
			name:  "ntsc frames keep microseconds",
			input: 30000.0 / 1001,
			expectedKeep: []Interval{
				{Start: 0.934267, End: 3.003}, {Start: 9.009, End: 9.976633},
			},
			expectedCuts: []Cut{{Start: 3.003, End: 5.005, Reason: FillerCutReason, Text: testFiller}},
			expectedFPS:  29.97,
			expectedDur:  3.036,
			expectedCand: Interval{Start: 2.435767, End: 5.005},
		},
		{
			name:         "unknown frame rate changes nothing",
			input:        0,
			expectedKeep: plan.Keep,
			expectedCuts: plan.Cuts,
			expectedDur:  3.16,
			expectedCand: Interval{Start: 2.43, End: 5.001},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := SnapToFrames(&plan, tt.input)

			assert.Equal(t, tt.expectedKeep, got.Keep)
			assert.Equal(t, tt.expectedCuts, got.Cuts)
			assert.InDelta(t, tt.expectedFPS, got.FrameRate, 1e-9)
			assert.InDelta(t, tt.expectedDur, got.Duration, 1e-9)
			require.Len(t, got.Candidates, 1)
			assert.Equal(t, tt.expectedCand, got.Candidates[0].Cut)
		})
	}

	assert.Len(t, plan.Keep, 4, "the input plan is not changed")
}

func TestTargetFrameRate(t *testing.T) {
	t.Parallel()

	rate := func(num, den int64) ffmpeg.Rational { return ffmpeg.Rational{Num: num, Den: den} }

	tests := []struct {
		name     string
		input    ffmpeg.MediaInfo
		expected ffmpeg.Rational
	}{
		{name: "60 fps becomes 30", input: ffmpeg.MediaInfo{RealFrameRate: rate(60, 1)}, expected: rate(30, 1)},
		{name: "59.94 becomes 29.97", input: ffmpeg.MediaInfo{RealFrameRate: rate(60000, 1001)}, expected: rate(30000, 1001)},
		{name: "50 becomes 25", input: ffmpeg.MediaInfo{RealFrameRate: rate(50, 1)}, expected: rate(25, 1)},
		{name: "30 is kept", input: ffmpeg.MediaInfo{RealFrameRate: rate(30, 1)}, expected: rate(30, 1)},
		{name: "29.97 is kept", input: ffmpeg.MediaInfo{RealFrameRate: rate(30000, 1001)}, expected: rate(30000, 1001)},
		{name: "25 is kept", input: ffmpeg.MediaInfo{RealFrameRate: rate(25, 1)}, expected: rate(25, 1)},
		{name: "24 is kept", input: ffmpeg.MediaInfo{RealFrameRate: rate(24, 1)}, expected: rate(24, 1)},
		{name: "a rate under the cap is reduced", input: ffmpeg.MediaInfo{RealFrameRate: rate(60, 2)}, expected: rate(30, 1)},
		{name: "just above the cap becomes 30", input: ffmpeg.MediaInfo{RealFrameRate: rate(3000001, 100000)}, expected: rate(30, 1)},
		{name: "slow motion 120 becomes 30", input: ffmpeg.MediaInfo{RealFrameRate: rate(120, 1)}, expected: rate(30, 1)},
		{name: "40 would halve below 24, becomes 30", input: ffmpeg.MediaInfo{RealFrameRate: rate(40, 1)}, expected: rate(30, 1)},
		{
			name:     "variable frame rate: the average when the nominal is a timebase",
			input:    ffmpeg.MediaInfo{RealFrameRate: rate(600, 1), AvgFrameRate: rate(17988, 600)},
			expected: rate(1499, 50),
		},
		{
			name:     "variable 60 fps clip by its average",
			input:    ffmpeg.MediaInfo{RealFrameRate: rate(600, 1), AvgFrameRate: rate(35976, 600)},
			expected: rate(1499, 50),
		},
		{name: "unknown", input: ffmpeg.MediaInfo{}, expected: ffmpeg.Rational{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := targetFrameRate(&tt.input)
			assert.Equal(t, tt.expected, got)
			assert.LessOrEqual(t, got.Float(), float64(MaxFrameRate))
		})
	}
}
