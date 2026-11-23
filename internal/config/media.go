package config

import "time"

// Media configures incoming files of the team chat: their copy in S3 (internal/media).
type Media struct {
	// KeyPrefix of incoming files in the S3 bucket: <prefix>/<chat id>/<message id>/<file unique id>.<ext>.
	// Lower-case path segments of letters, digits, "-" and "_".
	KeyPrefix string `env:"MEDIA_KEY_PREFIX" envDefault:"incoming"`
	// ResultPrefix of the editor's results (the reel, its plan and transcript), laid out like the incoming files:
	// <prefix>/<chat id>/<message id>/<name>.<ext>. Must differ from MEDIA_KEY_PREFIX; empty disables results.
	ResultPrefix string `env:"MEDIA_RESULT_PREFIX" envDefault:"edits"`
	// UploadTimeout bounds copying one file to S3 (a video up to TELEGRAM_MAX_FILE_SIZE).
	UploadTimeout time.Duration `env:"MEDIA_UPLOAD_TIMEOUT" envDefault:"10m"`
}
