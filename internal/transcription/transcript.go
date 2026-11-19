package transcription

// Result is a transcript of a whole video. Times are in seconds from the start of the video.
type Result struct {
	// Language is the requested ISO 639-1 code, or the language Whisper detected when none was given.
	Language string    `json:"language"`
	Duration float64   `json:"duration"`
	Text     string    `json:"text"`
	Segments []Segment `json:"segments"`
	Words    []Word    `json:"words"`
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
