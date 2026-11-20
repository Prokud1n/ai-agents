package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatTimecode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    float64
		expected string
	}{
		{name: "zero", input: 0, expected: "0:00.0"},
		{name: "seconds", input: 12.34, expected: "0:12.3"},
		{name: "rounds up to a minute", input: 59.96, expected: "1:00.0"},
		{name: "minutes", input: 185.25, expected: "3:05.3"},
		{name: "negative is zero", input: -1, expected: "0:00.0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, FormatTimecode(tt.input))
		})
	}
}

func TestDecisionsLog(t *testing.T) {
	t.Parallel()

	src := speech("давайте при | давайте приступим || эээ к разминке")
	plan := BuildPlan(src, src.Duration, testSettings())
	plan.Source = testClip

	log := DecisionsLog(&plan)

	for _, expected := range []string{
		"Черновая склейка: clip.mp4",
		"Дубли: 1",
		"- Дубль 1 (0:00.5–0:01.4) оборван на «давайте при…», оставлен дубль 2",
		"Слова-паразиты: 1",
		"«эээ»",
		"Паузы: 1",
	} {
		assert.Contains(t, log, expected)
	}
}
