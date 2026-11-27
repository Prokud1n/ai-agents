package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWordCutJoints: only the joints where words were cut, on the timeline of the reel; not the pauses pressed to a
// breath.
func TestWordCutJoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    Plan
		expected []float64
	}{
		{
			name: "a take and a pause",
			input: Plan{
				Keep: []Interval{{Start: 1, End: 3}, {Start: 3.5, End: 5}, {Start: 8, End: 9}},
				Cuts: []Cut{
					{Start: 0, End: 1, Reason: PauseCutReason},
					{Start: 3, End: 3.5, Reason: PauseCutReason},
					{Start: 5, End: 8, Reason: RetakeCutReason},
				},
			},
			expected: []float64{3.5},
		},
		{
			name: "the editor's cut and a filler",
			input: Plan{
				Keep: []Interval{{Start: 0, End: 2}, {Start: 4, End: 6}, {Start: 6.4, End: 7}},
				Cuts: []Cut{{Start: 2, End: 4, Reason: EditCutReason}, {Start: 6, End: 6.4, Reason: FillerCutReason}},
			},
			expected: []float64{2, 4},
		},
		{
			name: "a pause cut in pieces with words between",
			input: Plan{
				Keep: []Interval{{Start: 0, End: 2}, {Start: 5, End: 6}},
				Cuts: []Cut{
					{Start: 2, End: 3, Reason: PauseCutReason},
					{Start: 3, End: 4, Reason: EditCutReason},
					{Start: 4, End: 5, Reason: PauseCutReason},
				},
			},
			expected: []float64{2},
		},
		{
			name:  "only pauses",
			input: Plan{Keep: []Interval{{Start: 0, End: 2}, {Start: 3, End: 4}}, Cuts: []Cut{{Start: 2, End: 3, Reason: PauseCutReason}}},
		},
		{name: "no keep", input: Plan{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, WordCutJoints(&tt.input))
		})
	}
}
