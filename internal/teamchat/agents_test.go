package teamchat

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/knowledge/kbtool"
)

// TestEveryRoleHasAgent: a task of a message goes to the agent named after the target role, so the real
// agents/ must have an agent for every bot that is not answered with a fixed text (stubTexts) and is not the
// editor (its tasks are videos, edited by the media worker without a model).
func TestEveryRoleHasAgent(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	tools := []agents.Tool{kbtool.NewSearchTool(nil, nil, slog.New(slog.DiscardHandler))}

	registry, err := agents.LoadRegistry(filepath.Join(root, "agents"), filepath.Join(root, "skills"), tools)
	require.NoError(t, err)

	for _, role := range gateway.Roles {
		if _, stub := stubTexts[role]; stub || role == gateway.EditorRole {
			continue
		}

		assert.Contains(t, registry.Names(), string(role))
	}

	assert.Len(t, stubTexts, 1, "the designer is not connected yet")
}
