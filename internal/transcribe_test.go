package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

func TestDeclareTranscribeOptions(t *testing.T) {
	t.Parallel()

	var svc *transcription.Service

	err := fx.ValidateApp(append(DeclareTranscribeOptions(), fx.Populate(&svc))...)

	require.NoError(t, err)
}
