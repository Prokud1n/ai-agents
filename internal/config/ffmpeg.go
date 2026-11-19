package config

import "time"

type FFmpeg struct {
	// Limit for one ffmpeg/ffprobe run (extracting, probing, cutting one file).
	Timeout time.Duration `env:"FFMPEG_TIMEOUT" envDefault:"10m"`
}
