package whisper

type TranscribeData struct {
	// Path to an audio file in a format the API accepts (mp3, m4a, wav, ...), at most 25 MB.
	Path string
	// Language is an ISO 639-1 code; empty means the client default (WHISPER_LANGUAGE).
	Language string
	// Prompt is optional context, e.g. the tail of the previous chunk of a long recording.
	Prompt string
}
