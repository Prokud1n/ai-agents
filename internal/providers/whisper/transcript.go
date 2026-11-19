package whisper

import (
	"encoding/json"
	"fmt"
)

// Transcript is the verbose_json response of /audio/transcriptions. Times are in seconds
// from the start of the uploaded file.
type Transcript struct {
	// Whisper reports the language as an English name ("russian"), not as an ISO code.
	Language string    `json:"language"`
	Duration float64   `json:"duration"`
	Text     string    `json:"text"`
	Words    []Word    `json:"words"`
	Segments []Segment `json:"segments"`
}

type Word struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type Segment struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
	// Whisper hallucinates text on silence and music: such segments have a high no_speech_prob with
	// a low avg_logprob, or a high compression_ratio (the same phrase repeated).
	NoSpeechProb     float64 `json:"no_speech_prob"`
	AvgLogprob       float64 `json:"avg_logprob"`
	CompressionRatio float64 `json:"compression_ratio"`
}

func parseTranscript(body []byte) (*Transcript, error) {
	var t Transcript
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("failed to decode transcription response: %w", err)
	}

	return &t, nil
}
