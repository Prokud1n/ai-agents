package gateway

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// ErrBotsBroken: Telegram rejected the token of a bot (401); the bot is stopped until a restart.
var ErrBotsBroken = errors.New("telegram bots stopped: invalid token")

// Status is the gateway state for /readyz ("telegram"). Only an invalid token fails it: a Bot API outage is
// retried and is not a reason to report the service as not ready. /readyz shows only ok/fail.
type Status struct {
	mu     sync.Mutex
	broken []Role
}

func NewStatus() *Status {
	return &Status{}
}

func (s *Status) Ping(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.broken) == 0 {
		return nil
	}

	return fmt.Errorf("%w: %v", ErrBotsBroken, s.broken)
}

func (s *Status) markBroken(role Role) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !slices.Contains(s.broken, role) {
		s.broken = append(s.broken, role)
	}
}
