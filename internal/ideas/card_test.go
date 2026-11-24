package ideas

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validCard() Card {
	return Card{
		Title:   "Зарядка за 5 минут",
		Summary: "Короткий комплекс для тех, кто не успевает в зал.",
		Format:  WorkoutFormat,
		Hook:    "Нет времени на зал? Дайте мне пять минут.",
	}
}

func TestValidateCard(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    func(c *Card)
		expected func(c *Card)
		errText  string
	}{
		{name: "valid", input: func(*Card) {}, expected: func(*Card) {}},
		{
			name:     "spaces and line breaks collapse",
			input:    func(c *Card) { c.Title = "  Зарядка \n за 5 минут " },
			expected: func(c *Card) { c.Title = "Зарядка за 5 минут" },
		},
		{
			name:     "format note only for other",
			input:    func(c *Card) { c.FormatNote = "сторис" },
			expected: func(*Card) {},
		},
		{
			name:     "other with a note",
			input:    func(c *Card) { c.Format, c.FormatNote = OtherFormat, "сторис с опросом" },
			expected: func(c *Card) { c.Format, c.FormatNote = OtherFormat, "сторис с опросом" },
		},
		{name: "other without a note", input: func(c *Card) { c.Format = OtherFormat }, errText: "format_note пустое"},
		{name: "empty title", input: func(c *Card) { c.Title = " " }, errText: "title пустое"},
		{name: "empty summary", input: func(c *Card) { c.Summary = "" }, errText: "summary пустое"},
		{name: "empty hook", input: func(c *Card) { c.Hook = "" }, errText: "hook пустое"},
		{
			name:    "long title",
			input:   func(c *Card) { c.Title = strings.Repeat("я", MaxTitleLength+1) },
			errText: "title длиннее 80 символов",
		},
		{
			name:    "long summary",
			input:   func(c *Card) { c.Summary = strings.Repeat("я", MaxSummaryLength+1) },
			errText: "summary длиннее 600 символов",
		},
		{
			name:    "long hook",
			input:   func(c *Card) { c.Hook = strings.Repeat("я", MaxHookLength+1) },
			errText: "hook длиннее 200 символов",
		},
		{name: "unknown format", input: func(c *Card) { c.Format = "reels" }, errText: `format "reels" неизвестен`},
		{
			name:    "invisible characters",
			input:   func(c *Card) { c.Hook = "Привет​мир" },
			errText: "hook содержит управляющие или невидимые символы",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input := validCard()
			tt.input(&input)

			card, err := ValidateCard(&input)
			if tt.errText != "" {
				require.ErrorIs(t, err, ErrValidation)

				var check *CheckError

				require.ErrorAs(t, err, &check)
				assert.Contains(t, check.Message, tt.errText)

				return
			}

			require.NoError(t, err)

			expected := validCard()
			tt.expected(&expected)
			assert.Equal(t, expected, card)
		})
	}
}

func TestFormatCard(t *testing.T) {
	t.Parallel()

	other := validCard()
	other.Format, other.FormatNote = OtherFormat, "сторис с опросом"

	tests := []struct {
		name     string
		input    Card
		expected string
	}{
		{
			name:  "workout",
			input: validCard(),
			expected: "Идея: Зарядка за 5 минут\n\nО чём: Короткий комплекс для тех, кто не успевает в зал.\n" +
				"Формат: рилс-тренировка\nХук: «Нет времени на зал? Дайте мне пять минут.»",
		},
		{
			name:  "other format with its note",
			input: other,
			expected: "Идея: Зарядка за 5 минут\n\nО чём: Короткий комплекс для тех, кто не успевает в зал.\n" +
				"Формат: другое — сторис с опросом\nХук: «Нет времени на зал? Дайте мне пять минут.»",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, FormatCard(&tt.input))
		})
	}
}

func TestFormatNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    Format
		expected string
	}{
		{name: "talk", input: TalkFormat, expected: "разговорный рилс"},
		{name: "workout", input: WorkoutFormat, expected: "рилс-тренировка"},
		{name: "carousel", input: CarouselFormat, expected: "карусель"},
		{name: "other without a note", input: OtherFormat, expected: "другое"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, FormatName(&Card{Format: tt.input}))
		})
	}
}

func TestUpdaters(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		updater  UpdaterFn
		input    Status
		expected Status
		err      error
	}{
		{name: "done from new", updater: UpdateToClosed(DoneStatus, now), input: NewStatus, expected: DoneStatus},
		{name: "dropped from planned", updater: UpdateToClosed(DroppedStatus, now), input: PlannedStatus, expected: DroppedStatus},
		{name: "done twice", updater: UpdateToClosed(DoneStatus, now), input: DoneStatus, err: ErrClosed},
		{name: "drop a done idea", updater: UpdateToClosed(DroppedStatus, now), input: DoneStatus, err: ErrClosed},
		{name: "close to new", updater: UpdateToClosed(NewStatus, now), input: PlannedStatus, err: ErrValidation},
		{name: "planned from new", updater: UpdateToPlanned(now), input: NewStatus, expected: PlannedStatus},
		{name: "planned stays planned", updater: UpdateToPlanned(now), input: PlannedStatus, expected: PlannedStatus},
		{name: "planned from dropped", updater: UpdateToPlanned(now), input: DroppedStatus, err: ErrClosed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			updated, err := tt.updater(Idea{ID: 1, Status: tt.input})
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, updated.Status)
		})
	}
}
