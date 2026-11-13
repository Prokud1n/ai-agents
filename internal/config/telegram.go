package config

type Telegram struct {
	// Bot API server URL. With the local server (compose profile "telegram") getFile returns
	// a local path inside the shared telegram-bot-api-data volume, mounted read-only into app.
	BotAPIURL string `env:"TELEGRAM_BOT_API_URL" envDefault:"https://api.telegram.org"`
}
