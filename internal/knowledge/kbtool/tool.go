// Package kbtool exposes the brand knowledge base search to agents as the search_knowledge_base tool.
// It lives apart from knowledge, so the knowledge package and the kb command do not depend on agents.
package kbtool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/providers/embeddings"
)

// Name is the tool name agents list in `tools:` of agent.md.
const Name = "search_knowledge_base"

const (
	description = "Ищет в базе знаний бренда тренера фрагменты по смыслу: голос и тон, целевая аудитория, " +
		"продукты и цены, брендбук, лучшие посты. Вызывай перед любым текстом от имени тренера и перед тем, " +
		"как упоминать факты о ней, её продуктах или аудитории. Возвращает фрагменты с источником (файл) " +
		"и типом документа; используй факты только из найденного."
	// inputSchema: the limit bounds are knowledge.DefaultLimit and knowledge.MaxLimit (checked by a test).
	inputSchema = `{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Что нужно найти, своими словами, например: как мы говорим о питании"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20, "description": "Сколько фрагментов вернуть, по умолчанию 5"},
    "kinds": {
      "type": "array",
      "items": {"type": "string", "enum": ["brand", "product", "post", "other"]},
      "description": "Искать только в документах этих типов: brand — голос, ЦА, брендбук; product — продукты; post — лучшие посты; other — остальное. Пусто — во всех"
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`

	// callTimeout bounds one call: embedding the query (with its retries) and the database search.
	callTimeout = 30 * time.Second
	// maxQueryRunes bounds the query: a search query is a phrase, not a document.
	maxQueryRunes = 1000
	// maxFragmentRunes caps one fragment: a regular chunk (about 2000 runes plus overlap and heading) fits whole.
	maxFragmentRunes = 2500
	// maxOutputRunes caps the fragments of one call (about 4k tokens), so a search cannot eat the context:
	// with many results every fragment gets an equal share.
	maxOutputRunes = 16000
	truncatedMark  = "… [фрагмент обрезан]"

	logKeyQuery = "query"
)

// Texts the model sees. Errors go back as tool_result with is_error (agents.ToolError), so the run continues.
const (
	unavailableText = "База знаний сейчас недоступна: она не настроена (нет ключа эмбеддингов OPENAI_API_KEY). " +
		"Не выдумывай факты о тренере, её продуктах и аудитории: пиши без них или прямо скажи, что факты " +
		"не проверены по базе знаний."
	timeoutText = "База знаний не ответила за %s. Попробуй ещё раз один раз; если снова не выйдет — " +
		"не выдумывай факты и скажи, что база знаний не ответила."
	internalErrorText = "Поиск по базе знаний не удался из-за внутренней ошибки (подробности в логах сервиса). " +
		"Не выдумывай факты: продолжай без них или скажи, что база знаний недоступна."
	notFoundText = "В базе знаний ничего не найдено по запросу «%s». Переформулируй запрос или убери фильтр kinds. " +
		"Если и так ничего нет — не придумывай факты, а скажи, что в базе знаний этого нет."
	foundHeader     = "Найдено фрагментов: %d. В строке над фрагментом — его источник (файл в базе знаний) и тип документа."
	truncatedFooter = "Длинные фрагменты обрезаны, чтобы ответ поместился; для подробностей сделай более узкий запрос."
)

var errUnavailable = errors.New("knowledge base is unavailable")

type searcher interface {
	Search(ctx context.Context, data knowledge.SearchChunksData) ([]knowledge.SearchResult, error)
}

type embeddingsStatus interface {
	Configured() bool
}

// SearchTool is the search_knowledge_base agent tool.
type SearchTool struct {
	search     searcher
	embeddings embeddingsStatus
	log        *slog.Logger
	timeout    time.Duration
}

func NewSearchTool(s searcher, e embeddingsStatus, log *slog.Logger) *SearchTool {
	return &SearchTool{search: s, embeddings: e, log: log, timeout: callTimeout}
}

func (*SearchTool) Name() string {
	return Name
}

func (*SearchTool) Description() string {
	return description
}

func (*SearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(inputSchema)
}

// Available reports a knowledge base without embeddings: the agents registry warns about agents
// that list the tool.
func (t *SearchTool) Available() error {
	if !t.embeddings.Configured() {
		return fmt.Errorf("%w: %w", errUnavailable, embeddings.ErrNotConfigured)
	}

	return nil
}

// Execute searches and returns fragments with their sources and kinds. Bad arguments, an unavailable
// knowledge base, a timeout and infrastructure errors come back as agents.ToolError with a text for the
// model; only a cancelled run returns a plain error. Nothing found is a normal result, not an error.
func (t *SearchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	if err := t.Available(); err != nil {
		return "", agents.NewToolError(unavailableText)
	}

	data, err := parseInput(input)
	if err != nil {
		return "", err
	}

	searchCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	results, err := t.search.Search(searchCtx, data)
	if err != nil {
		return "", t.searchError(ctx, &data, err)
	}

	if len(results) == 0 {
		return fmt.Sprintf(notFoundText, data.Query), nil
	}

	return FormatResults(results), nil
}

// searchError turns a search error into a text for the model and logs the ones the model cannot fix.
func (t *SearchTool) searchError(ctx context.Context, data *knowledge.SearchChunksData, err error) error {
	switch {
	case ctx.Err() != nil:
		// The run itself is cancelled: nobody reads the tool result.
		return fmt.Errorf("failed to search knowledge base: %w", err)
	case errors.Is(err, knowledge.ErrValidation):
		return agents.NewToolError("Неверные аргументы: " + err.Error())
	case errors.Is(err, embeddings.ErrNotConfigured):
		return agents.NewToolError(unavailableText)
	case errors.Is(err, context.DeadlineExceeded):
		t.log.ErrorContext(ctx, "kbtool: knowledge base search timed out",
			"error", err, "timeout", t.timeout, logKeyQuery, data.Query)

		return agents.NewToolError(fmt.Sprintf(timeoutText, t.timeout))
	default:
		t.log.ErrorContext(ctx, "kbtool: failed to search knowledge base",
			"error", err, logKeyQuery, data.Query, "limit", data.Limit, "kinds", data.Kinds)

		return agents.NewToolError(internalErrorText)
	}
}

type searchInput struct {
	Query string   `json:"query"`
	Limit *int     `json:"limit"`
	Kinds []string `json:"kinds"`
}

// parseInput decodes and validates the arguments; every error is an agents.ToolError the model can fix.
func parseInput(input json.RawMessage) (knowledge.SearchChunksData, error) {
	var in searchInput

	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&in); err != nil {
		return knowledge.SearchChunksData{}, agents.NewToolError(fmt.Sprintf(
			`Аргументы должны быть объектом {"query": "...", "limit": 1-%d, "kinds": ["brand", ...]}: %v`,
			knowledge.MaxLimit, err))
	}

	query := strings.TrimSpace(in.Query)
	if query == "" {
		return knowledge.SearchChunksData{}, agents.NewToolError("query пустой: опиши своими словами, что нужно найти.")
	}

	if utf8.RuneCountInString(query) > maxQueryRunes {
		return knowledge.SearchChunksData{}, agents.NewToolError(fmt.Sprintf(
			"query слишком длинный: до %d символов, опиши коротко, что нужно найти.", maxQueryRunes))
	}

	limit, err := parseLimit(in.Limit)
	if err != nil {
		return knowledge.SearchChunksData{}, err
	}

	kinds, err := parseKinds(in.Kinds)
	if err != nil {
		return knowledge.SearchChunksData{}, err
	}

	return knowledge.SearchChunksData{Query: query, Limit: limit, Kinds: kinds}, nil
}

func parseLimit(limit *int) (int, error) {
	if limit == nil {
		return knowledge.DefaultLimit, nil
	}

	if *limit < 1 || *limit > knowledge.MaxLimit {
		return 0, agents.NewToolError(fmt.Sprintf("limit должен быть от 1 до %d (по умолчанию %d), получено %d.",
			knowledge.MaxLimit, knowledge.DefaultLimit, *limit))
	}

	return *limit, nil
}

func parseKinds(names []string) ([]knowledge.Kind, error) {
	kinds := make([]knowledge.Kind, 0, len(names))

	for _, name := range names {
		kind, err := knowledge.ParseKind(name)
		if err != nil {
			return nil, agents.NewToolError(fmt.Sprintf("Неизвестный тип %q в kinds, допустимы: %s.", name, kindNames()))
		}

		kinds = append(kinds, kind)
	}

	return kinds, nil
}

func kindNames() string {
	names := make([]string, 0, len(knowledge.Kinds))
	for _, k := range knowledge.Kinds {
		names = append(names, string(k))
	}

	return strings.Join(names, ", ")
}

// FormatResults renders found fragments for the model: a header, then every fragment with its source,
// title, kind and score (knowledge.FormatSearchResults). Fragments are cut so that their total stays
// within maxOutputRunes.
func FormatResults(results []knowledge.SearchResult) string {
	cut, truncated := truncateResults(results)

	var sb strings.Builder

	fmt.Fprintf(&sb, foundHeader, len(cut))
	sb.WriteString("\n\n")
	sb.WriteString(knowledge.FormatSearchResults(cut))

	if truncated {
		sb.WriteString("\n\n")
		sb.WriteString(truncatedFooter)
	}

	return sb.String()
}

// truncateResults returns copies of the results with every fragment cut to an equal share of
// maxOutputRunes (at most maxFragmentRunes), and whether any fragment was cut.
func truncateResults(results []knowledge.SearchResult) ([]knowledge.SearchResult, bool) {
	if len(results) == 0 {
		return nil, false
	}

	perFragment := min(maxFragmentRunes, maxOutputRunes/len(results))
	cut := make([]knowledge.SearchResult, 0, len(results))
	truncated := false

	for i := range results {
		r := results[i]

		content, wasCut := truncateRunes(r.Content, perFragment)
		r.Content = content
		truncated = truncated || wasCut

		cut = append(cut, r)
	}

	return cut, truncated
}

// truncateRunes cuts s to at most limit runes including truncatedMark; limit must exceed the mark.
func truncateRunes(s string, limit int) (string, bool) {
	if utf8.RuneCountInString(s) <= limit {
		return s, false
	}

	keep := limit - utf8.RuneCountInString(truncatedMark)
	runes := []rune(s)

	return strings.TrimRightFunc(string(runes[:keep]), unicode.IsSpace) + truncatedMark, true
}
