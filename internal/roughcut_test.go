package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

func TestDeclareRoughCutOptions(t *testing.T) {
	t.Parallel()

	var (
		cut *roughcut.Service
		tr  *transcription.Service
	)

	err := fx.ValidateApp(append(DeclareRoughCutOptions(), fx.Populate(&cut, &tr))...)

	require.NoError(t, err)
}
