package config

import (
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEditorDefaults: by default a talking head is shortened to a reel of up to 60 s (the trainer's own reel of a
// 3:56 source is 52 s), there is no end card and no top line until the texts are set; the top line comes from the
// environment as it is.
func TestEditorDefaults(t *testing.T) {
	t.Parallel()

	var defaults Editor
	require.NoError(t, env.ParseWithOptions(&defaults, env.Options{Environment: map[string]string{}}))

	assert.Equal(t, time.Minute, defaults.ReelsTargetDuration)
	assert.False(t, defaults.ReelsEndCard)
	assert.True(t, defaults.ReelsHandleIcon)
	assert.Empty(t, defaults.ReelsTopbarLeft)
	assert.Empty(t, defaults.ReelsTopbarRight)

	var set Editor
	require.NoError(t, env.ParseWithOptions(&set, env.Options{Environment: map[string]string{
		"REELS_TOPBAR_LEFT": "tg: @ana_prokudina", "REELS_TOPBAR_RIGHT": "2x", "EDITOR_REELS_TARGET_DURATION": "0s",
	}}))

	assert.Equal(t, "tg: @ana_prokudina", set.ReelsTopbarLeft)
	assert.Equal(t, "2x", set.ReelsTopbarRight)
	assert.Zero(t, set.ReelsTargetDuration, "0 turns the selection off")
}
