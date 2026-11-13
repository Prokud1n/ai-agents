# Code Style

Правила основаны на практиках сервиса `core-disputes`. Всё, что может проверить линтер, проверяется линтером (`golangci-lint` v2), остальное — на ревью.

## 1. Инструменты

- Форматирование: `gofmt` + `goimports` (`golangci-lint fmt ./...`).
- Линт: `golangci-lint run ./...` — должен проходить без ошибок перед каждым коммитом.
- Стандартные команды — через `Makefile`: `make run`, `make build`, `make test`, `make lint`, `make fmt`, `make generate`.
- `interface{}` не используем — только `any` (правило `gofmt rewrite`).

## 2. Структура проекта

```
main.go              — только создание и запуск приложения
internal/            — весь код сервиса
  app.go             — сборка fx-модулей (DeclareApplicationOptions)
  config/            — конфигурация из env, по файлу на подсистему
  <domain>/          — пакет на доменную сущность
  providers/<name>/  — клиенты внешних сервисов
pkg/helpers/         — маленькие generic-хелперы без бизнес-логики
migrations/          — SQL-миграции: NNN-описание.up.sql
```

Типичное содержимое доменного пакета:

| Файл | Что внутри |
|------|-----------|
| `fx.go` | `var Module = fx.Module(...)` — регистрация провайдеров |
| `<entity>.go` | модель, типы-перечисления, константы |
| `service.go` | бизнес-логика (`Service`, `NewService`) |
| `repo.go` | доступ к БД (неэкспортируемый `repo`, `newRepo`) |
| `dto.go` | входные структуры методов сервиса (`CreateXxxData`, `FindXxxData`) |
| `actions.go` | правила доступа / проверки допустимости действий |

`main.go` минимален:

```go
func main() {
	application := internal.NewApplication()
	application.Run()
}
```

## 3. Dependency Injection (Uber FX)

- Каждый пакет экспортирует `Module` в `fx.go`; все модули собираются в `internal/app.go`.
- Внутренние зависимости пакета регистрируем с `fx.Private` (например, `fx.Provide(newRepo, fx.Private)`).
- Конструкторы — `NewXxx(...) *Xxx`. При большом числе зависимостей — структура параметров с `fx.In`:

```go
type HandlerParams struct {
	fx.In

	Log     logger.LoggerInterface
	Service DisputeService
}
```

- `init()` запрещены (`gochecknoinits`).

## 4. Конфигурация

- Только через переменные окружения (`caarlos0/env`), теги `env` и `envDefault`.
- Одна структура на подсистему, отдельный файл в `internal/config/`.
- Полный список переменных держим в `.env.dist`; `.env` в git не коммитим.

```go
type PayoutAdminAPI struct {
	Address string `env:"PAYOUT_API_ADMIN_ADDRESS" envDefault:"localhost:50014"`
	Token   string `env:"PAYOUT_API_ADMIN_TOKEN"`
}
```

## 5. Импорты

Три группы через пустую строку: стандартная библиотека → внешние модули → модули текущего проекта (`goimports` с `local-prefixes`).

```go
import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
)
```

Алиас — только при конфликте имён или неочевидном имени пакета (`payinreqflow`, `logger`).

## 6. Именование

- Пакеты: коротко, в нижнем регистре, без `_` (`payoutapi`, `disputeerr`, `reqflow`).
- Перечисления — именованный строковый тип + константы с суффиксом типа:

```go
type Status string

const (
	InProgressStatus Status = "IN_PROGRESS"
	DeclinedStatus   Status = "DECLINED"
)
```

- Входные структуры сервисов: `<Действие><Сущность>Data` (`CreateDisputeData`, `FindDisputesData`).
- Аббревиатуры пишем заглавными: `ID`, `API`, `URL`, `SLA` (`PayoutID`, `GetByDocumentID`).
- Получатель метода — одна-две буквы (`s *Service`, `r *repo`, `h *Handler`); неиспользуемый получатель опускаем: `func (*Service) validate(...)`.

## 7. Интерфейсы

- Интерфейс объявляет **потребитель**, а не реализация, и только с нужными ему методами.
- Внутрипакетные интерфейсы — неэкспортируемые (`documentProvider`, `disputeProvider`).

```go
type disputeProvider interface {
	FindInProgressDisputes(ctx context.Context) ([]base.Dispute, error)
}
```

## 8. Ошибки

- Всегда оборачиваем с контекстом через `%w`, формулировка — `failed to <действие>`:

```go
if err != nil {
	return nil, fmt.Errorf("failed to get payout: %w", err)
}
```

- Доменные ошибки — sentinel-значения в отдельном пакете (`ErrNotFound`, `ErrValidation`, `ErrPermissionDenied`, `ErrConflict`…) с кодом; к ним добавляем контекст через обёртку:

```go
return fmt.Errorf("fiat real amount should be positive: %w", apperr.ErrValidation)
```

- Сравнение — только `errors.Is` / `errors.As` (`errorlint`).
- Ошибку БД «не найдено» превращаем в доменную `ErrNotFound` на уровне репозитория.
- Некритичные ошибки (не должны ломать основной сценарий) логируем и продолжаем:

```go
deadlines, err := s.slaService.GetDeadlines(ctx, &d.Base)
if err != nil {
	s.logger.Error("failed to resolve SLA deadlines", err, logger.F{"payout_id": req.PayoutID})
} else {
	// ...
}
```

- `panic` — только при старте приложения в `MustXxx`-функциях.

## 9. Функции и поток управления

- Ранний возврат вместо `else` после `return` (`revive: indent-error-flow`).
- Не более 50 statements в функции (`funlen`), цикломатическая сложность ≤ 15 (`gocyclo`).
- Длинные сигнатуры переносим по одному параметру на строку:

```go
func getChangedDisputes(
	disputes []base.Dispute,
	docsByDispute map[uint][]document.Document,
	now time.Time,
) []base.Dispute {
```

- Без именованных результатов с «голым» `return` (`nakedret`).
- Текущее время передаём параметром (`now time.Time`) в чистые функции — это упрощает тесты.

## 10. Работа со слайсами

- **Не мутируем входные данные в циклах.** Не берём `&slice[i]` для изменения — копируем элемент, работаем с копией и складываем результат в новый слайс:

```go
changed := make([]base.Dispute, 0, len(disputes))
for i := range disputes {
	d := disputes[i]
	d.Status = newStatus
	changed = append(changed, d)
}
```

- Если размер известен — preallocate: `make([]T, 0, len(src))`.
- Указатель на значение — встроенный `new(expr)` вместо хелперов `Ptr(...)`.

## 11. Числа и константы

- Магические числа запрещены (`mnd`), кроме 0–3 — выносим в именованные константы:

```go
const (
	ratioPrecision = 20
	defaultLimit   = 1000
)
```

- Строковые литералы, повторяющиеся 3+ раз, — тоже в константы (`goconst`).

## 12. База данных и репозитории

- Репозиторий — неэкспортируемый тип `repo`, наружу торчит через `Service`.
- Чтение — `GetReadConnection()`, запись — `GetWriteConnection()`.
- Изменения, требующие проверки состояния, — в транзакции с `SELECT ... FOR UPDATE`: загрузили → провалидировали → обновили → перечитали.
- Изменения описываем функциями-апдейтерами (`UpdaterFn`), которые возвращаются фабриками `UpdateToXxx(...)`.
- Схема меняется только миграциями, файлы нумеруются последовательно и не редактируются после мержа.

## 13. Логирование

- Логгер внедряется через DI (`logger.LoggerInterface`), глобальных логгеров нет.
- Сообщение — короткое, в нижнем регистре; данные — структурированными полями, а не через форматирование строки:

```go
w.logger.Info("worker: updated dispute statuses", logger.F{"count": len(changed)})
```

## 14. Контекст

- `ctx context.Context` — первый параметр всех методов, которые делают I/O.
- HTTP-запросы — только с контекстом (`noctx`), тела ответов закрываем (`bodyclose`).

## 15. Комментарии и директивы

- Комментарии — по делу: зачем, а не что. Короткие пояснения к значениям констант — в конце строки.
- `TODO` допустим, `FIXME` — нет (`godox`): FIXME исправляем до мержа.
- `//nolint` — только с указанием линтера и причиной (`nolintlint`):

```go
CoinID: uint32(uc.CoinID), //nolint:gosec // value always fits uint32
```

## 16. Тесты

- Табличные тесты со структурой `name / input / expected` и `t.Run`.
- `t.Parallel()` в тесте и в каждом подтесте, если нет общего состояния.
- Проверки — `testify/assert` (`require`, если дальнейший код без результата бессмыслен).
- Тестируем в первую очередь чистые функции с бизнес-логикой.

```go
func TestMapStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    Status
		expected string
	}{
		{name: "in progress", input: InProgressStatus, expected: "IN_PROGRESS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, MapStatus(tt.input))
		})
	}
}
```

## 17. Git и релизы

- Ветки: `feature/<TASK-ID>-<краткое-описание>`, `fix/...`, `refactoring/...`.
- Сообщения коммитов — на английском, кратко о сути: `TASK-ID added worker`, `Refactor find filters`.
- `CHANGELOG.md` в формате [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), версии по [SemVer](https://semver.org/). Каждая фича добавляет запись в `[Unreleased]` (Added / Changed / Fixed); релиз — отдельный коммит `Release vX.Y.Z`.
