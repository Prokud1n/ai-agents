package config

import "time"

// Telegram configures the Telegram gateway (internal/gateway). Without bot tokens the gateway is off and
// the application starts without it.
type Telegram struct {
	// Bot API server URL. With the local server (compose profile "telegram") getFile returns
	// a local path inside the shared telegram-bot-api-data volume, mounted read-only into app.
	BotAPIURL string `env:"TELEGRAM_BOT_API_URL" envDefault:"http://telegram-bot-api:8081"`

	// Tokens of the four bots from @BotFather. All four are required once any one is set.
	MarketerToken   string `env:"TELEGRAM_MARKETER_TOKEN"`
	CopywriterToken string `env:"TELEGRAM_COPYWRITER_TOKEN"`
	EditorToken     string `env:"TELEGRAM_EDITOR_TOKEN"`
	DesignerToken   string `env:"TELEGRAM_DESIGNER_TOKEN"`

	// GroupID is the team supergroup (negative, -100…); messages from other chats are ignored.
	GroupID int64 `env:"TELEGRAM_GROUP_ID"`
	// AllowedUserIDs: only these users are heard; comma-separated.
	AllowedUserIDs []int64 `env:"TELEGRAM_ALLOWED_USER_IDS"`

	// Forum topic IDs (message_thread_id); 0 means the topic is not configured.
	PlanningTopicID   int64 `env:"TELEGRAM_TOPIC_PLANNING_ID"`
	SourcesTopicID    int64 `env:"TELEGRAM_TOPIC_SOURCES_ID"`
	ProductionTopicID int64 `env:"TELEGRAM_TOPIC_PRODUCTION_ID"`
	TechTopicID       int64 `env:"TELEGRAM_TOPIC_TECH_ID"`

	// FilesDir is where the local Bot API server keeps files (the volume mounted into app);
	// paths from getFile outside it are rejected.
	FilesDir string `env:"TELEGRAM_FILES_DIR" envDefault:"/var/lib/telegram-bot-api"`
	// OutboxDir: Sender uploads files only from here or from FilesDir. Producers of drafts and covers write
	// their files here. Inside the app container (/tmp is writable in the distroless image).
	OutboxDir string `env:"TELEGRAM_OUTBOX_DIR" envDefault:"/tmp/telegram-outbox"`
	// MaxFileSize in bytes; larger media are not downloaded. Default 2 GiB.
	MaxFileSize int64 `env:"TELEGRAM_MAX_FILE_SIZE" envDefault:"2147483648"`

	// PollTimeout of getUpdates long polling (whole seconds, at least 1s).
	PollTimeout    time.Duration `env:"TELEGRAM_POLL_TIMEOUT"    envDefault:"50s"`
	RequestTimeout time.Duration `env:"TELEGRAM_REQUEST_TIMEOUT" envDefault:"30s"`
	// FileTimeout limits getFile (the local server downloads the file first) and uploads.
	FileTimeout    time.Duration `env:"TELEGRAM_FILE_TIMEOUT"     envDefault:"15m"`
	MaxRetries     int           `env:"TELEGRAM_MAX_RETRIES"      envDefault:"3"`
	RetryBaseDelay time.Duration `env:"TELEGRAM_RETRY_BASE_DELAY" envDefault:"1s"`
	RetryMaxDelay  time.Duration `env:"TELEGRAM_RETRY_MAX_DELAY"  envDefault:"30s"`
	// Workers is how many incoming events are handled at once (a media download blocks one worker).
	Workers int `env:"TELEGRAM_WORKERS" envDefault:"4"`
}

// Enabled reports whether any bot token is set: the gateway runs only then.
func (t *Telegram) Enabled() bool {
	return t.MarketerToken != "" || t.CopywriterToken != "" || t.EditorToken != "" || t.DesignerToken != ""
}
