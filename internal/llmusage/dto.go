package llmusage

import "time"

type RecordUsageData struct {
	Agent     string
	Model     string
	RequestID string
	Tokens    Tokens
	Duration  time.Duration
	CreatedAt time.Time
}
