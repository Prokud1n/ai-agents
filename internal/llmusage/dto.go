package llmusage

import "time"

type RecordUsageData struct {
	Agent string
	// Model is the requested model: the cost is calculated by its price.
	Model string
	// ResponseModel is the model reported in the response; stored for reference only.
	ResponseModel string
	RequestID     string
	Tokens        Tokens
	Duration      time.Duration
	CreatedAt     time.Time
}
