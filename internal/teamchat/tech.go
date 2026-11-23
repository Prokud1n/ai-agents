package teamchat

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
)

type techSender interface {
	SendText(ctx context.Context, role gateway.Role, msg *gateway.OutgoingMessage) (int64, error)
}

// TechReporter tells the team about a technical failure in «Техничка» (TELEGRAM_TOPIC_TECH_ID) from the agent's
// bot, while the trainer gets a plain text. The callers give a short text with the error code and the task: no
// secrets, paths or error texts (they are in the logs). Without the gateway or the topic it only logs.
type TechReporter struct {
	sender  techSender
	enabled bool
	topicID int64
	log     *slog.Logger
}

func NewTechReporter(cfg *config.Telegram, sender techSender, log *slog.Logger) *TechReporter {
	return &TechReporter{sender: sender, enabled: cfg.Enabled() && cfg.TechTopicID != 0, topicID: cfg.TechTopicID, log: log}
}

// Report sends the text to «Техничка» from the agent's bot.
func (r *TechReporter) Report(ctx context.Context, agent, text string) error {
	text = strings.TrimSpace(text)

	if !r.enabled {
		r.log.WarnContext(ctx, "teamchat: «Техничка» is not configured, the report goes to the log only",
			"agent", agent, "report", text)

		return nil
	}

	if _, err := r.sender.SendText(ctx, roleOf(agent), &gateway.OutgoingMessage{TopicID: r.topicID, Text: text}); err != nil {
		return fmt.Errorf("failed to report to the tech topic: %w", err)
	}

	return nil
}
