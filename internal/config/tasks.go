package config

import "time"

// Tasks configures the task queue and its workers.
type Tasks struct {
	// Workers is the number of tasks run at the same time.
	Workers int `env:"TASKS_WORKERS" envDefault:"2"`
	// MaxAttempts is the number of runs of one version before the task fails; permanent errors
	// (a refusal, max_tokens) fail it at once.
	MaxAttempts int `env:"TASKS_MAX_ATTEMPTS" envDefault:"3"`
	// Retry delay = base * 2^(attempt-1), at most max.
	RetryBaseDelay time.Duration `env:"TASKS_RETRY_BASE_DELAY" envDefault:"30s"`
	RetryMaxDelay  time.Duration `env:"TASKS_RETRY_MAX_DELAY"  envDefault:"10m"`
	// Timeout of one run; a run older than Timeout plus a grace minute is considered dead and restarted.
	Timeout time.Duration `env:"TASKS_TIMEOUT" envDefault:"15m"`
	// ShutdownGrace is how long running tasks may finish on stop before they are interrupted and
	// returned to the queue; it is shortened to fit APP_STOP_TIMEOUT.
	ShutdownGrace time.Duration `env:"TASKS_SHUTDOWN_GRACE" envDefault:"10s"`
	// SweepInterval: how often lost tasks are found in PostgreSQL and queued again.
	SweepInterval time.Duration `env:"TASKS_SWEEP_INTERVAL" envDefault:"1m"`
	// DeliveryAttempts is the number of attempts to send a draft (with the retry delays above) before
	// the task fails.
	DeliveryAttempts int `env:"TASKS_DELIVERY_ATTEMPTS" envDefault:"5"`
	// Stream is the Redis stream of the queue; delayed retries are in the sorted set <stream>:delayed.
	Stream string `env:"TASKS_STREAM" envDefault:"tasks"`
}
