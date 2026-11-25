package editjob

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/subtitles"
)

func TestBrandingValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handle  string
		invalid bool
	}{
		{name: "the trainer's handle", handle: "@prokudina.ana"},
		{name: "empty", handle: "", invalid: true},
		{name: "markup", handle: "@a}{\\N", invalid: true},
		{name: "a space", handle: "@prokudina ana", invalid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := (&Branding{Handle: tt.handle}).Validate()
			if tt.invalid {
				require.ErrorIs(t, err, subtitles.ErrInvalidHandle)

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestJobBrandingJSON: the handle goes to the worker in the job; a job without one keeps the JSON of before.
func TestJobBrandingJSON(t *testing.T) {
	t.Parallel()

	job := Job{ID: JobID(1, 1), Branding: &Branding{Handle: "@prokudina.ana", Icon: true, EndCard: true}}

	raw, err := json.Marshal(&job)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"branding":{"handle":"@prokudina.ana","icon":true,"end_card":true}`)

	var decoded Job
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, job.Branding, decoded.Branding)

	plain, err := json.Marshal(&Job{ID: JobID(1, 1)})
	require.NoError(t, err)
	assert.NotContains(t, string(plain), "branding")
}
