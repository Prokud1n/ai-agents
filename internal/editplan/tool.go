package editplan

import (
	"context"
	"encoding/json"

	"github.com/Prokud1n/ai-agents/internal/agents"
)

// SubmitToolName is the tool the editor answers with.
const SubmitToolName = "submit_edit"

// SubmitTool is submit_edit: the editor's structured decision, checked against the video of the run's session
// (WithSession). Outside a session (cmd/agent-run) it answers that it works only in an editing task.
type SubmitTool struct{}

func NewSubmitTool() *SubmitTool {
	return &SubmitTool{}
}

func (*SubmitTool) Name() string {
	return SubmitToolName
}

func (*SubmitTool) Description() string {
	return "Записывает решение по монтажу рилс: что вырезать, что вернуть, какие спорные повторы вырезать, какие " +
		"слова исправить в субтитрах — или отказ (cannot — такое не умеешь, unclear — не понял, что менять). " +
		"Индексы слов — из списка слов в задаче. Вызывай ровно один раз; если решение не прошло проверку, " +
		"исправь его по ответу и вызови ещё раз."
}

func (*SubmitTool) InputSchema() json.RawMessage {
	return json.RawMessage(inputSchema)
}

func (*SubmitTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	session, ok := sessionFrom(ctx)
	if !ok {
		return "", agents.NewToolError(noSessionText)
	}

	return session.submit(input)
}

// inputSchema of submit_edit; Input is its Go form.
const inputSchema = `{
  "type": "object",
  "properties": {
    "decision": {
      "type": "string",
      "enum": ["edit", "cannot", "unclear"],
      "description": "edit — изменения ниже (могут быть пустыми, если менять нечего); cannot — тренер просит то, чего монтаж не умеет; unclear — непонятно, что менять."
    },
    "reason": {
      "type": "string",
      "description": "Коротко, почему так решено: попадёт в лог решений монтажа."
    },
    "trim_start_seconds": {
      "type": "number",
      "minimum": 0,
      "description": "Убрать начало текущего ролика: столько секунд по времени ролика (слова, начатые раньше, вырезаются)."
    },
    "trim_end_seconds": {
      "type": "number",
      "minimum": 0,
      "description": "Убрать конец текущего ролика: столько последних секунд по времени ролика."
    },
    "remove_words": {
      "type": "array",
      "description": "Вырезать слова: отрезки индексов from–to включительно.",
      "items": {
        "type": "object",
        "properties": {
          "from": {"type": "integer", "minimum": 0},
          "to": {"type": "integer", "minimum": 0}
        },
        "required": ["from", "to"],
        "additionalProperties": false
      }
    },
    "restore_words": {
      "type": "array",
      "description": "Вернуть вырезанные слова (паузы, паразиты, дубли, прошлые правки): отрезки индексов from–to включительно.",
      "items": {
        "type": "object",
        "properties": {
          "from": {"type": "integer", "minimum": 0},
          "to": {"type": "integer", "minimum": 0}
        },
        "required": ["from", "to"],
        "additionalProperties": false
      }
    },
    "candidates": {
      "type": "array",
      "description": "Решения по спорным повторам из списка: cut=true вырезает первую фразу и оставляет повтор, cut=false оставляет обе.",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "integer"},
          "cut": {"type": "boolean"},
          "reason": {"type": "string"}
        },
        "required": ["id", "cut", "reason"],
        "additionalProperties": false
      }
    },
    "fix_words": {
      "type": "array",
      "description": "Исправить слово в субтитрах: индекс слова и правильное написание (одно слово или короткое имя).",
      "items": {
        "type": "object",
        "properties": {
          "index": {"type": "integer", "minimum": 0},
          "text": {"type": "string", "minLength": 1, "maxLength": 40}
        },
        "required": ["index", "text"],
        "additionalProperties": false
      }
    }
  },
  "required": ["decision", "reason"],
  "additionalProperties": false
}`
