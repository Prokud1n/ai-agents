package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/subtitles"
)

func TestDeclareSubtitlesOptions(t *testing.T) {
	t.Parallel()

	var svc *subtitles.Service

	err := fx.ValidateApp(append(DeclareSubtitlesOptions(), fx.Populate(&svc))...)

	require.NoError(t, err)
}
