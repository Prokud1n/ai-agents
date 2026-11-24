package editplan

import (
	"context"
	"encoding/json"

	"github.com/Prokud1n/ai-agents/internal/agents"
)

// SubmitToolName is the tool the editor answers with.
const SubmitToolName = "submit_edit"

// SubmitTool is submit_edit: the editor's structured decision, checked against the video of the run's session
// (WithSession). Outside a session (cmd/agent-run) it answers that it works only in an editing task. It is a final
// tool: once it answers without an error the decision is made and the run ends (agents.Runtime).
type SubmitTool struct{}

func NewSubmitTool() *SubmitTool {
	return &SubmitTool{}
}

func (*SubmitTool) Name() string {
	return SubmitToolName
}

func (*SubmitTool) Description() string {
	return "Записывает решение по монтажу рилс: режим (разговорный или тренировка), что вырезать, что вернуть, какие " +
		"спорные повторы вырезать, какие слова исправить в субтитрах, подписи упражнений, звук исходника — или отказ " +
		"(cannot — такое не умеешь, unclear — не понял, что менять). " +
		"Индексы слов — из списка слов в задаче. Вызывай ровно один раз; если решение не прошло проверку, " +
		"исправь его по ответу и вызови ещё раз."
}

func (*SubmitTool) InputSchema() json.RawMessage {
	return json.RawMessage(inputSchema)
}

// EndsRun: the decision is in the session, the model's next turn is not needed.
func (*SubmitTool) EndsRun() bool {
	return true
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
      "description": "Убрать начало: столько секунд. Для подписи к исходнику — по времени исходника, для правки — по времени текущего ролика. Вырезаются слова, середина которых попадает в эти секунды."
    },
    "trim_end_seconds": {
      "type": "number",
      "minimum": 0,
      "description": "Убрать конец: столько последних секунд. Для подписи к исходнику — по времени исходника, для правки — по времени текущего ролика."
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
    "mode": {
      "type": "string",
      "enum": ["talk", "workout"],
      "description": "Режим монтажа. talk — разговорный ролик: паузы, паразиты и неудачные дубли вырезаются. workout — тренировка (показ упражнений, часто молча): ролик остаётся целиком, режется только то, что указано явно. Не указывай, чтобы оставить текущий режим."
    },
    "cut_seconds": {
      "type": "array",
      "description": "Вырезать отрезки по времени (секунды: для подписи — исходника, для правки — текущего ролика): подготовка, отдых, лишнее между упражнениями.",
      "items": {
        "type": "object",
        "properties": {
          "from": {"type": "number", "minimum": 0},
          "to": {"type": "number", "minimum": 0}
        },
        "required": ["from", "to"],
        "additionalProperties": false
      }
    },
    "restore_cuts": {
      "type": "array",
      "description": "Вернуть вырезанные по времени отрезки прошлых версий: номера из списка «Вырезанные по времени отрезки».",
      "items": {"type": "integer", "minimum": 0}
    },
    "add_titles": {
      "type": "array",
      "description": "Добавить подписи упражнений поверх видео («Выпады — 12 раз»). Начало — word (индекс слова, где тренер называет упражнение) или start_seconds; конец — end_seconds или, без него, до следующей подписи. Время не выдумывай: если момент упражнения не понять ни из речи, ни из подписи, перенеси его в untimed_titles.",
      "items": {
        "type": "object",
        "properties": {
          "text": {"type": "string", "minLength": 1, "maxLength": 40},
          "word": {"type": "integer", "minimum": 0},
          "start_seconds": {"type": "number", "minimum": 0},
          "end_seconds": {"type": "number", "minimum": 0}
        },
        "required": ["text"],
        "additionalProperties": false
      }
    },
    "change_titles": {
      "type": "array",
      "description": "Изменить подписи упражнений по номеру из списка «Подписи упражнений»: новый текст и/или новое время (заданные поля заменяют старые).",
      "items": {
        "type": "object",
        "properties": {
          "index": {"type": "integer", "minimum": 0},
          "text": {"type": "string", "maxLength": 40},
          "word": {"type": "integer", "minimum": 0},
          "start_seconds": {"type": "number", "minimum": 0},
          "end_seconds": {"type": "number", "minimum": 0}
        },
        "required": ["index"],
        "additionalProperties": false
      }
    },
    "remove_titles": {
      "type": "array",
      "description": "Убрать подписи упражнений: номера из списка «Подписи упражнений».",
      "items": {"type": "integer", "minimum": 0}
    },
    "untimed_titles": {
      "type": "array",
      "description": "Упражнения из подписи тренера, момент которых не удалось определить: подпись не ставится, тренер получит подсказку добавить её правкой с временем.",
      "items": {"type": "string", "minLength": 1, "maxLength": 40}
    },
    "mute": {
      "type": "boolean",
      "description": "true — убрать звук исходника (и субтитры речи), например если в зале играла музыка; false — вернуть звук. Не указывай, чтобы оставить как есть."
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
