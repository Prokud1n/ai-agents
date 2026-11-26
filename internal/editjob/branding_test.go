package editjob

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrandingValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handle  string
		invalid bool
	}{
		{name: "the trainer's handle", handle: topbarHandle},
		{name: "empty", handle: "", invalid: true},
		{name: "markup", handle: "@a}{\\N", invalid: true},
		{name: "a space", handle: "@prokudina ana", invalid: true},
		{name: "digits and an underscore", handle: "@fit_2026"},
		{name: "no @", handle: "prokudina.ana", invalid: true},
		{name: "capitals", handle: "@Prokudina", invalid: true},
		{name: "a line break", handle: "@ana\n", invalid: true},
		{name: "cyrillic", handle: "@анастасия", invalid: true},
		{name: "too long", handle: "@" + strings.Repeat("a", 31), invalid: true},
		{name: "only @", handle: "@", invalid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := (&Branding{Handle: tt.handle}).Validate()
			if tt.invalid {
				require.ErrorIs(t, err, ErrInvalidHandle)

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestJobBrandingJSON: the handle goes to the worker in the job; a job without one keeps the JSON of before.
func TestJobBrandingJSON(t *testing.T) {
	t.Parallel()

	job := Job{ID: JobID(1, 1), Branding: &Branding{Handle: topbarHandle, Icon: true, EndCard: true}}

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

// Texts of the top line and the handle in the tests.
const (
	topbarTG     = "tg: @ana_prokudina"
	topbarSpeed  = "2x"
	topbarHandle = "@prokudina.ana"
)

// TestCheckTopbar: a text of the top line is at most 40 printable characters without markup; empty is nothing drawn.
func TestCheckTopbar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		invalid bool
	}{
		{name: "the trainer's telegram", text: topbarTG},
		{name: "the speed", text: topbarSpeed},
		{name: "empty", text: ""},
		{name: "cyrillic and an emoji", text: "телеграм: ана 🔥"},
		{name: "forty characters", text: strings.Repeat("a", 40)},
		{name: "too long", text: strings.Repeat("a", 41), invalid: true},
		{name: "ASS markup", text: "tg: {\\an7}", invalid: true},
		{name: "a backslash", text: "2x\\N", invalid: true},
		{name: "angle brackets", text: "<b>2x</b>", invalid: true},
		{name: "a line break", text: "2x\nREC", invalid: true},
		{name: "an invisible character", text: "2\u200bx", invalid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckTopbar(tt.text)
			if tt.invalid {
				require.ErrorIs(t, err, ErrInvalidTopbar)

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestBrandingValidateTopbar: branding may have the top line alone; a bad handle or a bad text of the top line fails
// it.
func TestBrandingValidateTopbar(t *testing.T) {
	t.Parallel()

	require.NoError(t, (&Branding{TopbarLeft: topbarTG, TopbarRight: topbarSpeed}).Validate())
	require.NoError(t, (&Branding{Handle: topbarHandle, TopbarRight: topbarSpeed}).Validate())
	require.ErrorIs(t, (&Branding{Handle: "@Ana", TopbarLeft: "tg"}).Validate(), ErrInvalidHandle)
	require.ErrorIs(t, (&Branding{TopbarLeft: "tg {x}"}).Validate(), ErrInvalidTopbar)
	require.ErrorIs(t, (&Branding{}).Validate(), ErrInvalidHandle, "nothing to draw")
}
