package roughcut

import "github.com/Prokud1n/ai-agents/internal/transcription"

type CutData struct {
	// Source is a local video file in any format ffmpeg reads.
	Source string
	// Transcript of the source (cmd/transcribe or transcription.Service).
	Transcript *transcription.Result
	// Plan, if set, is rendered as it is (an edited plan.json) instead of building a new one.
	Plan *Plan
	// AcceptCandidates are IDs of Plan.Candidates of the same transcript and settings to cut as well.
	AcceptCandidates []int
	// Settings for a new plan; nil means the configured defaults.
	Settings *Settings
	// OutDir receives plan.json, transcript.json, decisions.log and roughcut.mp4; it is created if missing.
	OutDir string
	// PlanOnly skips rendering: only the plan, the remapped transcript and the log are written.
	PlanOnly bool
}
