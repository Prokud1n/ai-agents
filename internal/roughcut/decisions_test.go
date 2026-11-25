package roughcut

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestHookInEditAndLog: the hook title is kept by the plan's copy of the edit, written to the decisions log, and an
// edit without one keeps the JSON of the plans made before the hook.
func TestHookInEditAndLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		hook     string
		expected string
	}{
		{name: "with a hook", hook: "Три ошибки в планке", expected: "Заголовок-хук: «Три ошибки в планке»"},
		{name: "without one", hook: "", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			edit := &Edit{Hook: tt.hook}
			cloned := cloneEdit(edit)
			assert.Equal(t, tt.hook, cloned.Hook)

			src := speech("давайте приступим к разминке")
			plan := BuildPlan(src, src.Duration, testSettings())
			plan.Edit = cloned

			log := DecisionsLog(&plan)
			raw, err := json.Marshal(cloned)
			require.NoError(t, err)

			if tt.expected == "" {
				assert.NotContains(t, log, "Заголовок-хук")
				assert.NotContains(t, string(raw), "hook")

				return
			}

			assert.Contains(t, log, tt.expected)
			assert.Contains(t, string(raw), `"hook":"Три ошибки в планке"`)
		})
	}
}
