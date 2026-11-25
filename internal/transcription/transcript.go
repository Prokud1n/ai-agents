package transcription

import "github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"

// Result is a transcript of a whole video. Times are in seconds from the start of the video.
type Result struct {
	// Language is the requested ISO 639-1 code, or the language Whisper detected when none was given.
	Language string    `json:"language"`
	Duration float64   `json:"duration"`
	Text     string    `json:"text"`
	Segments []Segment `json:"segments"`
	Words    []Word    `json:"words"`
	// Loudness of the source sound, measured while its audio was extracted: the render normalizes the draft by it
	// without measuring again (the stored transcript keeps it for the revisions). Nil if not measured.
	Loudness *ffmpeg.Loudness `json:"loudness,omitempty"`
}

type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type Word struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}
