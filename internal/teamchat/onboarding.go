package teamchat

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/pilot"
)

// pinRole pins the greetings: one bot needs the right to pin messages, whoever sent them.
const pinRole = gateway.MarketerRole

// greeting is the pinned greeting of a topic and the bot that sends it.
type greeting struct {
	topic   gateway.Topic
	topicID int64
	role    gateway.Role
	text    string
}

// greetings are the greetings of the configured topics: «Планёрка» from the marketer (he answers the commands and the
// text tasks), «Исходники» and «Продакшн» from the editor (videos go there and his video drafts come there). «Техничка»
// gets none: it is not the trainer's.
func greetings(cfg *config.Telegram) []greeting {
	all := []greeting{
		{topic: gateway.PlanningTopic, topicID: cfg.PlanningTopicID, role: gateway.MarketerRole, text: planningGreeting},
		{topic: gateway.SourcesTopic, topicID: cfg.SourcesTopicID, role: gateway.EditorRole, text: sourcesGreeting},
		{topic: gateway.ProductionTopic, topicID: cfg.ProductionTopicID, role: gateway.EditorRole, text: productionGreeting},
	}

	configured := make([]greeting, 0, len(all))

	for _, g := range all {
		if g.topicID != 0 {
			configured = append(configured, g)
		}
	}

	return configured
}

// adminSet is the set of TELEGRAM_ADMIN_USER_IDS; nil when it is empty.
func adminSet(ids []int64) map[int64]struct{} {
	if len(ids) == 0 {
		return nil
	}

	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}

	return set
}

func (h *Handler) isAdmin(userID int64) bool {
	if len(h.admins) == 0 {
		return true // the gateway lets in only TELEGRAM_ALLOWED_USER_IDS
	}

	_, ok := h.admins[userID]

	return ok
}

// onboard publishes the greetings of the topics and pins them. A repeated /onboard edits the stored greetings without a
// new pin (or sends a new one when the old message is gone or the topic changed, and unpins the old one), so the topics
// get no duplicates. A greeting that
// cannot be pinned is told to the admin and to «Техничка».
func (h *Handler) onboard(ctx context.Context, event *gateway.Event) error {
	switch {
	case event.Forwarded:
		return h.reply(ctx, gateway.MarketerRole, event, onboardForwardedText)
	case !h.isAdmin(event.From.ID):
		return h.reply(ctx, gateway.MarketerRole, event, onboardNotAllowedText)
	case len(h.greetings) == 0:
		return h.reply(ctx, gateway.MarketerRole, event, onboardNoTopicsText)
	}

	var pinned, noRights, failed []gateway.Topic

	for _, g := range h.greetings {
		err := h.publishGreeting(ctx, event.ChatID, &g)

		switch {
		case err == nil:
			pinned = append(pinned, g.topic)
		case errors.Is(err, gateway.ErrNoRights):
			h.log.WarnContext(ctx, "teamchat: no right to pin the greeting", "topic", g.topic, "error", err)
			noRights = append(noRights, g.topic)
		default:
			h.log.ErrorContext(ctx, "teamchat: failed to publish the greeting", "topic", g.topic, "error", err)
			failed = append(failed, g.topic)
		}
	}

	if len(noRights)+len(failed) > 0 {
		if err := h.tech.Report(ctx, string(gateway.MarketerRole), onboardTechText(noRights, failed)); err != nil {
			h.log.WarnContext(ctx, "teamchat: failed to report onboarding to the tech topic", "error", err)
		}
	}

	return h.reply(ctx, gateway.MarketerRole, event, onboardResultText(pinned, noRights, failed))
}

// publishGreeting makes the topic's greeting current and pinned. A stored greeting of the same bot and topic is edited
// and pinned only if it is not pinned yet (every pin puts a service message into the topic); otherwise a new greeting is
// sent, the old one unpinned.
func (h *Handler) publishGreeting(ctx context.Context, chatID int64, g *greeting) error {
	stored, err := h.pilot.OnboardingMessage(ctx, chatID, string(g.topic))

	switch {
	case err == nil && stored.Role == string(g.role) && stored.TopicID == g.topicID:
		edited, err := h.editGreeting(ctx, &stored, g)
		if err != nil {
			return err
		}

		if edited {
			return h.pinGreeting(ctx, &stored)
		}
	case err == nil: // another bot or another topic: only a new message can be the greeting
	case errors.Is(err, pilot.ErrNotFound):
		stored = pilot.OnboardingMessage{}
	default:
		return fmt.Errorf("failed to find the greeting: %w", err)
	}

	id, err := h.sender.SendText(ctx, g.role, &gateway.OutgoingMessage{TopicID: g.topicID, Text: g.text})
	if err != nil {
		return fmt.Errorf("failed to send the greeting: %w", err)
	}

	sent := pilot.OnboardingMessage{
		ChatID: chatID, Topic: string(g.topic), TopicID: g.topicID, Role: string(g.role), MessageID: id,
	}

	// Stored before the pin: a repeated /onboard after a failed pin edits this message instead of sending another.
	if err := h.pilot.SaveOnboardingMessage(ctx, &sent); err != nil {
		return fmt.Errorf("failed to store the greeting: %w", err)
	}

	if stored.MessageID != 0 && stored.Pinned {
		if err := h.sender.UnpinMessage(ctx, pinRole, stored.MessageID); err != nil {
			h.log.WarnContext(ctx, "teamchat: failed to unpin the old greeting", "topic", g.topic, "error", err)
		}
	}

	return h.pinGreeting(ctx, &sent)
}

// pinGreeting pins the greeting unless it is pinned already and remembers the pin.
func (h *Handler) pinGreeting(ctx context.Context, msg *pilot.OnboardingMessage) error {
	if msg.Pinned {
		return nil
	}

	if err := h.sender.PinMessage(ctx, pinRole, msg.MessageID); err != nil {
		return err
	}

	pinned := *msg
	pinned.Pinned = true

	if err := h.pilot.SaveOnboardingMessage(ctx, &pinned); err != nil {
		return fmt.Errorf("failed to store the pin of the greeting: %w", err)
	}

	return nil
}

// editGreeting puts the current text into the stored greeting; false when the message is gone and a new one is needed.
func (h *Handler) editGreeting(ctx context.Context, stored *pilot.OnboardingMessage, g *greeting) (bool, error) {
	err := h.sender.EditText(ctx, g.role, stored.MessageID, g.text, nil)

	switch {
	case err == nil, errors.Is(err, gateway.ErrNotModified):
		return true, nil
	case errors.Is(err, gateway.ErrMessageNotFound):
		h.log.InfoContext(ctx, "teamchat: the greeting was deleted, sending a new one", "topic", g.topic)

		return false, nil
	default:
		return false, fmt.Errorf("failed to edit the greeting: %w", err)
	}
}

// unknownAdmins are the IDs of TELEGRAM_ADMIN_USER_IDS that are not in TELEGRAM_ALLOWED_USER_IDS: the gateway never lets
// them in, so they cannot run /onboard.
func unknownAdmins(admins, allowed []int64) []int64 {
	var unknown []int64

	for _, id := range admins {
		if !slices.Contains(allowed, id) {
			unknown = append(unknown, id)
		}
	}

	return unknown
}
