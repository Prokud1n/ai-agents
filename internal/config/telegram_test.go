package config

import (
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTelegramParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    map[string]string
		expected Telegram
		enabled  bool
	}{
		{
			name: "empty values from .env.dist keep the gateway off",
			input: map[string]string{
				"TELEGRAM_MARKETER_TOKEN":    "",
				"TELEGRAM_GROUP_ID":          "",
				"TELEGRAM_ALLOWED_USER_IDS":  "",
				"TELEGRAM_ADMIN_USER_IDS":    "",
				"TELEGRAM_TOPIC_SOURCES_ID":  "",
				"TELEGRAM_TOPIC_PLANNING_ID": "",
			},
			expected: defaultTelegram(),
			enabled:  false,
		},
		{
			name: "configured gateway",
			input: map[string]string{
				"TELEGRAM_MARKETER_TOKEN":   "1:a",
				"TELEGRAM_GROUP_ID":         "-1001234567890",
				"TELEGRAM_ALLOWED_USER_IDS": "111,222",
				"TELEGRAM_ADMIN_USER_IDS":   "111",
				"TELEGRAM_TOPIC_SOURCES_ID": "7",
			},
			expected: func() Telegram {
				cfg := defaultTelegram()
				cfg.MarketerToken = "1:a"
				cfg.GroupID = -1001234567890
				cfg.AllowedUserIDs = []int64{111, 222}
				cfg.AdminUserIDs = []int64{111}
				cfg.SourcesTopicID = 7

				return cfg
			}(),
			enabled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var cfg Telegram

			err := env.ParseWithOptions(&cfg, env.Options{Environment: tt.input})

			require.NoError(t, err)
			assert.Equal(t, tt.expected, cfg)
			assert.Equal(t, tt.enabled, cfg.Enabled())
		})
	}
}

func defaultTelegram() Telegram {
	return Telegram{
		BotAPIURL:      "http://telegram-bot-api:8081",
		FilesDir:       "/var/lib/telegram-bot-api",
		OutboxDir:      "/tmp/telegram-outbox",
		MaxFileSize:    2 << 30,
		PollTimeout:    50 * time.Second,
		RequestTimeout: 30 * time.Second,
		FileTimeout:    15 * time.Minute,
		MaxRetries:     3,
		RetryBaseDelay: time.Second,
		RetryMaxDelay:  30 * time.Second,
		Workers:        4,
		MediaWorkers:   2,
	}
}
