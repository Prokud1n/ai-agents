package knowledge

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	noResultsText = "В базе знаний ничего не найдено по этому запросу."
	scoreDigits   = 2
	float64Bits   = 64
)

// FormatSearchResults renders results for a model or a human:
//
//	[1] brand/voice.md — «Голос бренда» (brand, score 0.83)
//	<fragment>
func FormatSearchResults(results []SearchResult) string {
	if len(results) == 0 {
		return noResultsText
	}

	var sb strings.Builder

	for i, r := range results {
		if i > 0 {
			sb.WriteString("\n\n")
		}

		fmt.Fprintf(&sb, "[%d] %s — «%s» (%s, score %s)\n%s",
			i+1, r.Source, r.Title, r.Kind, strconv.FormatFloat(r.Score, 'f', scoreDigits, float64Bits), r.Content)
	}

	return sb.String()
}

// FormatCorrections renders corrections as few-shot examples for an agent's prompt.
func FormatCorrections(corrections []Correction) string {
	var sb strings.Builder

	for i, c := range corrections {
		if i > 0 {
			sb.WriteString("\n\n")
		}

		fmt.Fprintf(&sb, "Пример %d (%s, %s)\nКак не надо:\n%s\nКак надо:\n%s", i+1, c.Agent, c.TaskKind, c.Original, c.Corrected)

		if c.Comment != "" {
			fmt.Fprintf(&sb, "\nКомментарий тренера: %s", c.Comment)
		}
	}

	return sb.String()
}
