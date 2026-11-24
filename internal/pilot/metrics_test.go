package pilot

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// testEditor is the editor agent.
const (
	testEditor     = "editor"
	testCopywriter = "copywriter"
	testMarketer   = "marketer"
)

var testStart = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

func row(agent string, status tasks.Status, version int, draftAfter time.Duration) TaskRow {
	r := TaskRow{Agent: agent, Status: status, Version: version, CreatedAt: testStart}
	if draftAfter >= 0 {
		r.FirstDraftAt = new(testStart.Add(draftAfter))
	}

	return r
}

func TestCompute(t *testing.T) {
	t.Parallel()

	const noDraft = -1

	tests := []struct {
		name     string
		input    []TaskRow
		expected []AgentMetrics
	}{
		{name: "no tasks", input: nil, expected: []AgentMetrics{}},
		{
			name: "editor: share of the first version, revisions, median of an even count",
			input: []TaskRow{
				row(testEditor, tasks.ApprovedStatus, 1, 10*time.Minute),
				row(testEditor, tasks.ApprovedStatus, 3, 20*time.Minute),
				row(testEditor, tasks.ApprovedStatus, 1, 30*time.Minute),
				row(testEditor, tasks.RejectedStatus, 2, 40*time.Minute),
				row(testEditor, tasks.QueuedStatus, 0, noDraft),
			},
			expected: []AgentMetrics{{
				Agent: testEditor, Created: 5, Approved: 3, ApprovedFirst: 2, Revisions: 2, Rejected: 1,
				FirstDrafts: 4, MedianFirstDraft: 25 * time.Minute, MaxFirstDraft: 40 * time.Minute,
			}},
		},
		{
			name: "agents in the fixed order, the others by name; median of an odd count",
			input: []TaskRow{
				row(testMarketer, tasks.AwaitingApprovalStatus, 1, 3*time.Minute),
				row("zeta", tasks.FailedStatus, 0, noDraft),
				row(testCopywriter, tasks.ApprovedStatus, 2, time.Minute),
				row(testCopywriter, tasks.ApprovedStatus, 1, 5*time.Minute),
				row(testCopywriter, tasks.ApprovedStatus, 1, 2*time.Minute),
				row("alpha", tasks.RejectedStatus, 1, time.Minute),
			},
			expected: []AgentMetrics{
				{
					Agent: testCopywriter, Created: 3, Approved: 3, ApprovedFirst: 2, Revisions: 1,
					FirstDrafts: 3, MedianFirstDraft: 2 * time.Minute, MaxFirstDraft: 5 * time.Minute,
				},
				{
					Agent: testMarketer, Created: 1, FirstDrafts: 1, MedianFirstDraft: 3 * time.Minute,
					MaxFirstDraft: 3 * time.Minute,
				},
				{
					Agent: "alpha", Created: 1, Rejected: 1, FirstDrafts: 1, MedianFirstDraft: time.Minute,
					MaxFirstDraft: time.Minute,
				},
				{Agent: "zeta", Created: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, Compute(tt.input))
		})
	}
}

func TestAgentMetricsRatios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		input           AgentMetrics
		expectedPercent int
		expectedTenths  int
	}{
		{name: "nothing approved", input: AgentMetrics{Created: 3}},
		{
			name:            "two of three from the first version, 2 revisions",
			input:           AgentMetrics{Approved: 3, ApprovedFirst: 2, Revisions: 2},
			expectedPercent: 67, expectedTenths: 7,
		},
		{
			name:            "all from the first version",
			input:           AgentMetrics{Approved: 4, ApprovedFirst: 4},
			expectedPercent: 100,
		},
		{
			name:            "1.5 revisions on average",
			input:           AgentMetrics{Approved: 2, Revisions: 3},
			expectedPercent: 0, expectedTenths: 15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expectedPercent, tt.input.FirstVersionPercent())
			assert.Equal(t, tt.expectedTenths, tt.input.AverageRevisionsTenths())
		})
	}
}

func TestMedianKeepsInput(t *testing.T) {
	t.Parallel()

	input := []time.Duration{3 * time.Minute, time.Minute, 2 * time.Minute}

	assert.Equal(t, 2*time.Minute, median(input))
	assert.Equal(t, []time.Duration{3 * time.Minute, time.Minute, 2 * time.Minute}, input)
}
