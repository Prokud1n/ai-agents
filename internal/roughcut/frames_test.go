package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
