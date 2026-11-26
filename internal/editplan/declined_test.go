package editplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

const (
	musicText = "Музыку добавьте в Instagram при публикации — я её не накладываю."
	speedText = "Ускорение и замедление я не делаю — видео идёт с обычной скоростью."
)

// TestApplyDeclined: the requests the editor does not do become the prepared texts — with the rest of the edit, with a
// refusal, or as the refusal of a revision that changes nothing else; an unknown kind or nothing to decline is a check
// failure.
func TestApplyDeclined(t *testing.T) {
	t.Parallel()

	withCaption := func(caption string) func() *Request {
		return func() *Request {
			req := testRequest(1, nil)
			req.Caption = caption

			return req
		}
	}

	revision := func() *Request {
		req := testRequest(2, &roughcut.Edit{})
		req.Revision = "добавь музыку и ускорь"

		return req
	}

	tests := []struct {
		name     string
		req      func() *Request
		input    Input
		kind     OutcomeKind
		declined []string
		err      string
	}{
		{
			name: "a caption: the rest is done", req: withCaption("добавь музыку и убери начало"),
			input: Input{Decision: EditDecision, TrimStart: 1, Declined: []DeclinedKind{SpeedDeclined, MusicDeclined, MusicDeclined}},
			kind:  EditOutcome, declined: []string{musicText, speedText},
		},
		{
			name: "a caption refused", req: withCaption("добавь музыку"),
			input: Input{Decision: CannotDecision, Declined: []DeclinedKind{MusicDeclined}},
			kind:  CannotOutcome, declined: []string{musicText},
		},
		{
			name: "a revision with nothing else: a refusal", req: revision,
			input: Input{Decision: EditDecision, Declined: []DeclinedKind{MusicDeclined, SpeedDeclined}},
			kind:  CannotOutcome, declined: []string{musicText, speedText},
		},
		{
			name: "a revision done in part", req: revision,
			input: Input{Decision: EditDecision, TrimEnd: 1, Declined: []DeclinedKind{MusicDeclined}},
			kind:  EditOutcome, declined: []string{musicText},
		},
		{
			name: "unclear keeps the declined", req: revision,
			input: Input{Decision: UnclearDecision, Declined: []DeclinedKind{MusicDeclined}},
			kind:  UnclearOutcome, declined: []string{musicText},
		},
		{
			name: "a revision that changes nothing without a decline", req: revision,
			input: Input{Decision: EditDecision}, err: hookNoChange,
		},
		{
			name: "an unknown kind", req: withCaption("сделай красиво"),
			input: Input{Decision: EditDecision, Declined: []DeclinedKind{"font"}}, err: "неизвестная категория",
		},
		{
			name: "nothing was asked", req: func() *Request { return testRequest(1, nil) },
			input: Input{Decision: EditDecision, Declined: []DeclinedKind{MusicDeclined}}, err: "только для просьб тренера",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in := tt.input
			in.Reason = testReason

			outcome, err := Apply(tt.req(), &in)
			if tt.err != "" {
				var check *CheckError
				require.ErrorAs(t, err, &check)
				assert.Contains(t, check.Message, tt.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.kind, outcome.Kind)
			assert.Equal(t, tt.declined, outcome.Declined)
		})
	}
}

func TestDeclinedTexts(t *testing.T) {
	t.Parallel()

	for _, kind := range declinedOrder {
		assert.NotEmpty(t, declinedTexts[kind], kind)
	}

	assert.Len(t, declinedTexts, len(declinedOrder), "every kind is in the order")
	assert.Equal(t, musicText+" "+speedText, DeclinedText([]string{musicText, speedText}))
	assert.Empty(t, DeclinedText(nil))
}
