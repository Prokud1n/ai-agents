package subtitles

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testHandle = "@prokudina.ana"
	cardEvent  = ",Card,,"
)

// TestBrandEvents: the handle over the reel (with the icon when on), the card over the last seconds (the frame
// darkens, the icon and the handle in the middle) and the handle fading out as it comes; nothing without a valid
// handle; no card for a reel of unknown length or too short.
func TestBrandEvents(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()

	tests := []struct {
		name        string
		branding    *Branding
		reelEnd     float64
		expected    []string
		notExpected []string
	}{
		{name: "none", reelEnd: 10, notExpected: []string{"Dialogue"}},
		{name: "an invalid handle", branding: &Branding{Handle: "@a{\\an7}"}, reelEnd: 10, notExpected: []string{"Dialogue"}},
		{
			name: "the handle alone", branding: &Branding{Handle: testHandle}, reelEnd: 10,
			expected:    []string{"Dialogue: 3,0:00:00.00,0:00:10.00,Handle,,0,0,0,,{\\q2}@prokudina.ana\n"},
			notExpected: []string{cardEvent, "\\p1"},
		},
		{
			name: "the icon and the card", branding: &Branding{Handle: testHandle, Icon: true, EndCard: true}, reelEnd: 10,
			expected: []string{
				"Dialogue: 3,0:00:00.00,0:00:08.00,Handle,,0,0,0,,{\\q2\\fad(0,500)}{\\1c&HFFFFFF&\\1a&H00&\\bord0" +
					"\\pbo5\\p1}m 9.5 0 l 24.5 0 ",
				"{\\p0\\r} {\\q2}@prokudina.ana\n",
				"Dialogue: 4,0:00:08.00,0:00:10.00,Card,,0,0,0,,{\\an7\\pos(0,0)\\fad(500,0)\\1c&H000000&\\1a&H4C&\\bord0" +
					"\\shad0\\p1}m 0 0 l 1080 0 l 1080 1920 l 0 1920{\\p0}\n",
				"Dialogue: 5,0:00:08.00,0:00:10.00,Card,,0,0,0,,{\\an5\\pos(540,960)\\fad(500,0)\\q2}{\\1c&HFFFFFF&\\1a&H00&",
				"{\\p0\\r}\\N{\\q2}@prokudina.ana\n",
			},
		},
		{
			name: "a reel too short for the card", branding: &Branding{Handle: testHandle, EndCard: true}, reelEnd: 2.5,
			expected:    []string{"Dialogue: 3,0:00:00.00,0:00:02.50,Handle,,0,0,0,,{\\q2}@prokudina.ana\n"},
			notExpected: []string{cardEvent},
		},
		{
			name: "a reel of unknown length", branding: &Branding{Handle: testHandle, EndCard: true},
			expected:    []string{"Dialogue: 3,0:00:00.00,10:00:00.00,Handle,"},
			notExpected: []string{cardEvent},
		},
		{
			name:     "a long handle is squeezed",
			branding: &Branding{Handle: "@" + strings.Repeat("a", 30)}, reelEnd: 10,
			expected: []string{"{\\q2}{\\fscx54}@aaaa"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := brandEvents(tt.branding, tt.reelEnd, &style)

			for _, part := range tt.expected {
				assert.Contains(t, got, part)
			}

			for _, part := range tt.notExpected {
				assert.NotContains(t, got, part)
			}
		})
	}
}

// TestCardTimes: the card takes its duration at the end of the reel, at most a third of it, leaving at least 2 s
// before it; none when off or for a reel of unknown length or too short.
func TestCardTimes(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	on := &Branding{Handle: testHandle, EndCard: true}

	tests := []struct {
		name     string
		branding *Branding
		reelEnd  float64
		expected [2]float64
	}{
		{name: "the duration at the end", branding: on, reelEnd: 10, expected: [2]float64{8, 2}},
		{name: "a third of a short reel", branding: on, reelEnd: 4.5, expected: [2]float64{3, 1.5}},
		{name: "too short", branding: on, reelEnd: 2.9},
		{name: "unknown length", branding: on},
		{name: "off", branding: &Branding{Handle: testHandle}, reelEnd: 10},
		{name: "no handle", branding: &Branding{EndCard: true}, reelEnd: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			start, duration := cardTimes(tt.branding, tt.reelEnd, &style)
			assert.InDelta(t, tt.expected[0], start, 1e-9)
			assert.InDelta(t, tt.expected[1], duration, 1e-9)
		})
	}
}

// TestIconPath: the rounded square and the circle are rings (an outer contour and an inner one), the dot is solid;
// everything is inside the icon's square.
func TestIconPath(t *testing.T) {
	t.Parallel()

	const size = 34.0

	path := iconPath(size)

	assert.Equal(t, 5, strings.Count(path, "m "), "two rings and a dot")

	for _, field := range strings.Fields(path) {
		if field == "m" || field == "l" || field == "b" {
			continue
		}

		value := parseFloat(t, field)
		assert.GreaterOrEqual(t, value, 0.0)
		assert.LessOrEqual(t, value, size)
	}
}

// TestValidateHandle: the handle stays clear of the Instagram top bar and the buttons, the hook, the words, the
// phrase subtitles and the titles; the card's values are checked.
func TestValidateHandle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		phrase      bool
		modify      func(s *Style)
		expectedErr string
	}{
		{name: "the default fits", modify: func(*Style) {}},
		{name: "the phrase default fits", phrase: true, modify: func(*Style) {}},
		{name: "over the hook", modify: func(s *Style) { s.Handle.Y = 300 }, expectedErr: overlapsHook},
		{name: "on the buttons", modify: func(s *Style) { s.Handle.Y = 1100 }, expectedErr: "above the buttons"},
		{name: "over the words", modify: func(s *Style) { s.Handle.Right = 400 }, expectedErr: "overlaps the words"},
		{
			name: "over the phrase subtitles under the hook", phrase: true, modify: func(s *Style) { s.Handle.Y = 620 },
			expectedErr: "overlaps the subtitles under the hook",
		},
		{name: "a card fade longer than the card", modify: func(s *Style) { s.EndCard.Fade = 3000 }, expectedErr: "end_card.duration"},
		{name: "a tiny icon", modify: func(s *Style) { s.Handle.IconSize = 5 }, expectedErr: "icon_size"},
		{name: "a translucent handle", modify: func(s *Style) { s.Handle.Color = "#FFFFFF33" }, expectedErr: "handle.color"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			style := DefaultStyle()
			if tt.phrase {
				style = PhraseStyle()
			}

			tt.modify(&style)

			err := style.Validate()
			if tt.expectedErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidStyle)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func parseFloat(t *testing.T, value string) float64 {
	t.Helper()

	f, err := strconv.ParseFloat(value, 64)
	require.NoError(t, err)

	return f
}
