package transcription

type TranscribeData struct {
	// Path to a local video or audio file in any format ffmpeg reads.
	Path string
	// Language is an ISO 639-1 code; empty means WHISPER_LANGUAGE.
	Language string
}
