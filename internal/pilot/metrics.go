package pilot

import (
	"slices"
	"time"

	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	percent = 100
	tenths  = 10
)

// agentOrder is the order of the agents in the metrics; other agents follow by name.
var agentOrder = []string{"editor", "copywriter", "marketer", "designer"}

// AgentMetrics are the metrics of one agent's tasks created in the period, by their current state.
type AgentMetrics struct {
	Agent    string
	Created  int
	Approved int
	// ApprovedFirst: approved at version 1, without revisions.
	ApprovedFirst int
	// Revisions of the approved tasks: every revision makes a new version, so it is version − 1 of each.
	Revisions int
	Rejected  int
	// FirstDrafts is the number of tasks with a first draft; MedianFirstDraft and MaxFirstDraft are the time from the
	// task to its first draft (the version is stored when the draft is ready, right before it is sent).
	FirstDrafts      int
	MedianFirstDraft time.Duration
	MaxFirstDraft    time.Duration
}

// FirstVersionPercent is the share of the approved tasks approved at the first version, rounded; 0 without approved.
func (m *AgentMetrics) FirstVersionPercent() int {
	if m.Approved == 0 {
		return 0
	}

	return (m.ApprovedFirst*percent + m.Approved/2) / m.Approved
}

// AverageRevisionsTenths is the average number of revisions before the approval in tenths, rounded: 15 is 1.5.
func (m *AgentMetrics) AverageRevisionsTenths() int {
	if m.Approved == 0 {
		return 0
	}

	return (m.Revisions*tenths + m.Approved/2) / m.Approved
}

// Compute counts the metrics of the tasks by agent: agentOrder first, then the others by name.
func Compute(rows []TaskRow) []AgentMetrics {
	byAgent := make(map[string]AgentMetrics)
	drafts := make(map[string][]time.Duration)

	for i := range rows {
		row := rows[i]
		m := byAgent[row.Agent]
		m.Agent = row.Agent
		m.Created++

		switch row.Status {
		case tasks.ApprovedStatus:
			m.Approved++
			m.Revisions += max(row.Version-1, 0)

			if row.Version == 1 {
				m.ApprovedFirst++
			}
		case tasks.RejectedStatus:
			m.Rejected++
		default:
		}

		if row.FirstDraftAt != nil {
			drafts[row.Agent] = append(drafts[row.Agent], max(row.FirstDraftAt.Sub(row.CreatedAt), 0))
		}

		byAgent[row.Agent] = m
	}

	result := make([]AgentMetrics, 0, len(byAgent))

	for _, agent := range orderedAgents(byAgent) {
		m := byAgent[agent]
		durations := drafts[agent]
		m.FirstDrafts = len(durations)
		m.MedianFirstDraft = median(durations)

		if len(durations) > 0 {
			m.MaxFirstDraft = slices.Max(durations)
		}

		result = append(result, m)
	}

	return result
}

func orderedAgents(byAgent map[string]AgentMetrics) []string {
	ordered := make([]string, 0, len(byAgent))

	for _, agent := range agentOrder {
		if _, ok := byAgent[agent]; ok {
			ordered = append(ordered, agent)
		}
	}

	others := make([]string, 0, len(byAgent))

	for agent := range byAgent {
		if !slices.Contains(agentOrder, agent) {
			others = append(others, agent)
		}
	}

	slices.Sort(others)

	return append(ordered, others...)
}

// median of the durations (the mean of the two middle ones for an even count); 0 for none. The input is not changed.
func median(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}

	sorted := slices.Clone(durations)
	slices.Sort(sorted)

	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}

	return (sorted[middle-1] + sorted[middle]) / 2
}
