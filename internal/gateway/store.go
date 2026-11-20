package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Prokud1n/ai-agents/internal/providers/redis"
)

const (
	offsetKeyPrefix = "telegram:offset:" // + bot ID
	seenKeyPrefix   = "telegram:seen:"   // + event key
	// seenTTL is above the 24 hours Telegram keeps unconfirmed updates.
	seenTTL = 48 * time.Hour
)

// stateStore keeps the polling offset of each bot and the events already taken, so a restart does not
// handle an update twice.
type stateStore interface {
	// LoadOffset returns the next update_id to request for the bot; 0 if none is stored.
	LoadOffset(ctx context.Context, botID int64) (int64, error)
	SaveOffset(ctx context.Context, botID, offset int64) error
	// MarkSeen records the key and reports whether it was new.
	MarkSeen(ctx context.Context, key string) (bool, error)
	// Unmark removes the key of an event that was not handled, so a repeated delivery is handled again. Telegram
	// delivers an update again only if the bot's offset was not saved yet; otherwise the event is lost (at most
	// once).
	Unmark(ctx context.Context, key string) error
}

type redisStore struct {
	rdb *goredis.Client
}

func newStateStore(client *redis.Client) stateStore {
	return &redisStore{rdb: client.Conn()}
}

func (s *redisStore) LoadOffset(ctx context.Context, botID int64) (int64, error) {
	value, err := s.rdb.Get(ctx, offsetKey(botID)).Result()
	if errors.Is(err, goredis.Nil) {
		return 0, nil
	}

	if err != nil {
		return 0, fmt.Errorf("failed to load telegram offset: %w", err)
	}

	offset, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse telegram offset: %w", err)
	}

	return offset, nil
}

func (s *redisStore) SaveOffset(ctx context.Context, botID, offset int64) error {
	if err := s.rdb.Set(ctx, offsetKey(botID), offset, 0).Err(); err != nil {
		return fmt.Errorf("failed to save telegram offset: %w", err)
	}

	return nil
}

func (s *redisStore) MarkSeen(ctx context.Context, key string) (bool, error) {
	isNew, err := s.rdb.SetNX(ctx, seenKeyPrefix+key, 1, seenTTL).Result()
	if err != nil {
		return false, fmt.Errorf("failed to mark telegram event as seen: %w", err)
	}

	return isNew, nil
}

func (s *redisStore) Unmark(ctx context.Context, key string) error {
	if err := s.rdb.Del(ctx, seenKeyPrefix+key).Err(); err != nil {
		return fmt.Errorf("failed to unmark telegram event: %w", err)
	}

	return nil
}

func offsetKey(botID int64) string {
	return offsetKeyPrefix + strconv.FormatInt(botID, 10)
}

// seenKey identifies an event across bots and restarts.
func seenKey(e *Event) string {
	if e.Callback != nil {
		return "callback:" + e.Callback.ID
	}

	return "message:" + strconv.FormatInt(e.ChatID, 10) + ":" + strconv.FormatInt(e.MessageID, 10)
}
