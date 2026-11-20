package gateway

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
)

// ErrInvalidConfig: the gateway is enabled (a token is set) but the config is incomplete.
var ErrInvalidConfig = errors.New("invalid telegram gateway config")

// bot is one team bot: its role, client and the public bot ID (from the token).
type bot struct {
	role   Role
	id     int64
	client *telegram.Client
}

// bots are the four team bots; empty when the gateway is disabled.
type bots struct {
	byRole map[Role]*bot
}

func (b *bots) enabled() bool {
	return len(b.byRole) > 0
}

// list returns the bots in the order of Roles.
func (b *bots) list() []*bot {
	list := make([]*bot, 0, len(b.byRole))
	for _, role := range Roles {
		if item, ok := b.byRole[role]; ok {
			list = append(list, item)
		}
	}

	return list
}

func newBots(cfg *config.Telegram, log *slog.Logger) (*bots, error) {
	if !cfg.Enabled() {
		return &bots{}, nil
	}

	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	tokens := roleTokens(cfg)
	result := &bots{byRole: make(map[Role]*bot, len(tokens))}

	for _, role := range Roles {
		id, err := telegram.BotIDFromToken(tokens[role])
		if err != nil {
			return nil, fmt.Errorf("failed to read %s bot token: %w", role, err)
		}

		result.byRole[role] = &bot{
			role: role,
			id:   id,
			client: telegram.NewClient(&telegram.Options{
				BaseURL:        cfg.BotAPIURL,
				Token:          tokens[role],
				RequestTimeout: cfg.RequestTimeout,
				FileTimeout:    cfg.FileTimeout,
				MaxRetries:     cfg.MaxRetries,
				RetryBaseDelay: cfg.RetryBaseDelay,
				RetryMaxDelay:  cfg.RetryMaxDelay,
			}, log.With("bot", string(role))),
		}
	}

	return result, nil
}

// tokenVariable is the name of the role's token variable, e.g. TELEGRAM_MARKETER_TOKEN.
func tokenVariable(role Role) string {
	return "TELEGRAM_" + strings.ToUpper(string(role)) + "_TOKEN"
}

func roleTokens(cfg *config.Telegram) map[Role]string {
	return map[Role]string{
		MarketerRole:   cfg.MarketerToken,
		CopywriterRole: cfg.CopywriterToken,
		EditorRole:     cfg.EditorToken,
		DesignerRole:   cfg.DesignerToken,
	}
}

// validateConfig checks an enabled gateway. Errors name the variables, never their values.
func validateConfig(cfg *config.Telegram) error {
	var errs []error

	seen := make(map[int64]Role, len(Roles))
	tokens := roleTokens(cfg)

	for _, role := range Roles {
		id, err := telegram.BotIDFromToken(tokens[role])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s is empty or not a bot token", tokenVariable(role)))

			continue
		}

		if other, dup := seen[id]; dup {
			errs = append(errs, fmt.Errorf("%s and %s have the same bot token", other, role))
		}

		seen[id] = role
	}

	if cfg.GroupID == 0 {
		errs = append(errs, errors.New("TELEGRAM_GROUP_ID is not set"))
	}

	if len(cfg.AllowedUserIDs) == 0 {
		errs = append(errs, errors.New("TELEGRAM_ALLOWED_USER_IDS is empty: nobody would be heard"))
	}

	if len(topicsOf(cfg)) < countNonZero(cfg.PlanningTopicID, cfg.SourcesTopicID, cfg.ProductionTopicID, cfg.TechTopicID) {
		errs = append(errs, errors.New("TELEGRAM_TOPIC_*_ID values must differ"))
	}

	if cfg.MaxFileSize <= 0 {
		errs = append(errs, errors.New("TELEGRAM_MAX_FILE_SIZE must be positive"))
	}

	if cfg.Workers <= 0 {
		errs = append(errs, errors.New("TELEGRAM_WORKERS must be positive"))
	}

	if cfg.PollTimeout < time.Second {
		errs = append(errs, errors.New("TELEGRAM_POLL_TIMEOUT must be at least 1s"))
	}

	if len(errs) > 0 {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, errors.Join(errs...))
	}

	return nil
}

// topicsOf maps configured topic IDs to named topics.
func topicsOf(cfg *config.Telegram) map[int64]Topic {
	configured := []struct {
		id    int64
		topic Topic
	}{
		{id: cfg.PlanningTopicID, topic: PlanningTopic},
		{id: cfg.SourcesTopicID, topic: SourcesTopic},
		{id: cfg.ProductionTopicID, topic: ProductionTopic},
		{id: cfg.TechTopicID, topic: TechTopic},
	}

	topics := make(map[int64]Topic, len(configured))

	for _, c := range configured {
		if c.id != 0 {
			topics[c.id] = c.topic
		}
	}

	return topics
}

func countNonZero(ids ...int64) int {
	n := 0

	for _, id := range ids {
		if id != 0 {
			n++
		}
	}

	return n
}
