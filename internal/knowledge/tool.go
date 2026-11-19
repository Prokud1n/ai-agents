package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Search exposed as an LLM tool. It does not depend on a tool registry: the registry (T1.4) can wrap
// RunSearchTool with SearchToolName, SearchToolDescription and SearchToolParameters.
const (
	SearchToolName        = "search_knowledge_base"
	SearchToolDescription = "Ищет в базе знаний бренда фрагменты по смыслу: голос и тон, целевая аудитория, " +
		"продукты, брендбук, лучшие посты. Используй перед тем, как писать текст от имени тренера."
	// SearchToolParameters is the JSON Schema of SearchToolInput.
	SearchToolParameters = `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Что нужно найти, своими словами, например: как мы говорим о питании"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20, "description": "Сколько фрагментов вернуть, по умолчанию 5"},
    "kinds": {
      "type": "array",
      "items": {"type": "string", "enum": ["brand", "product", "post", "other"]},
      "description": "Искать только в документах этих типов; пусто — во всех"
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`

	noResultsText = "В базе знаний ничего не найдено по этому запросу."
	scoreDigits   = 2
	float64Bits   = 64
)

type SearchToolInput struct {
	Query string   `json:"query"`
	Limit int      `json:"limit,omitempty"`
	Kinds []string `json:"kinds,omitempty"`
}

// RunSearchTool decodes tool-call arguments (JSON matching SearchToolParameters), searches and returns
// text for the model: fragments with their sources.
func (s *Service) RunSearchTool(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input SearchToolInput
	if err := json.Unmarshal(arguments, &input); err != nil {
		return "", fmt.Errorf("failed to decode %s arguments: %w: %w", SearchToolName, ErrValidation, err)
	}

	return s.SearchTool(ctx, input)
}

// SearchTool searches and formats the results as text with sources.
func (s *Service) SearchTool(ctx context.Context, input SearchToolInput) (string, error) {
	kinds := make([]Kind, 0, len(input.Kinds))
	for _, k := range input.Kinds {
		kind, err := ParseKind(k)
		if err != nil {
			return "", err
		}

		kinds = append(kinds, kind)
	}

	results, err := s.Search(ctx, SearchChunksData{Query: input.Query, Limit: input.Limit, Kinds: kinds})
	if err != nil {
		return "", err
	}

	return FormatSearchResults(results), nil
}

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
