package roughcut

// PlanVersion is the version of the plan.json format; it changes when a field changes its meaning.
const PlanVersion = 1

// CutReason says why a piece of the source was cut.
type CutReason string

const (
	// PauseCutReason is silence between words longer than Settings.MaxPause, or before the first and after
	// the last word.
	PauseCutReason CutReason = "pause"
	// FillerCutReason is a filler sound or hesitation ("э", "ммм") from Settings.Fillers.
	FillerCutReason CutReason = "filler"
	// RetakeCutReason is a failed take: a phrase that was broken off or said again right after.
	RetakeCutReason CutReason = "retake"
)

// TakeKind tells how a failed take was recognized.
type TakeKind string

const (
	// IncompleteTakeKind is a phrase broken off mid-word and restarted: "давайте при… давайте приступим".
	IncompleteTakeKind TakeKind = "incomplete"
	// RepeatedTakeKind is a phrase said again from its start, after a pause, with mostly the same words.
	RepeatedTakeKind TakeKind = "repeated"
	// AcceptedTakeKind is a candidate the editor chose to cut (CutData.AcceptCandidates).
	AcceptedTakeKind TakeKind = "accepted"
)

// CandidateReason says why a phrase said again was not cut.
type CandidateReason string

const (
	// RepeatCandidateReason is a finished phrase said again: a retake or an intended repeat.
	RepeatCandidateReason CandidateReason = "repeat"
	// RhythmCandidateReason is a chain of the same phrase or an enumeration ("вдох, выдох. Вдох, выдох").
	RhythmCandidateReason CandidateReason = "rhythm"
)

// Plan is the edit decision list of a rough cut: which intervals of the source are kept and why the rest
// was cut. All times are in seconds from the start of the source. Keep is what gets rendered: a person or
// an agent may edit it (and the plan is rendered again with -plan); Cuts and Takes explain the automatic plan.
type Plan struct {
	Version int    `json:"version"`
	Source  string `json:"source,omitempty"`
	// SourceDuration is the length of the source video, Duration the length of the result (sum of Keep).
	SourceDuration float64 `json:"source_duration"`
	Duration       float64 `json:"duration"`
	// FrameRate the video is converted to (rounded from the source); the edges of Keep and Cuts lie on its frame grid (see SnapToFrames).
	FrameRate float64    `json:"frame_rate,omitempty"`
	Settings  Settings   `json:"settings"`
	Keep      []Interval `json:"keep"`
	Cuts      []Cut      `json:"cuts"`
	Takes     []Take     `json:"takes"`
	// Candidates are repeats left in the cut: the editor (a person or the editing agent) decides whether
	// to cut the earlier phrase by editing Keep.
	Candidates []Candidate `json:"candidates"`
}

// Settings are the thresholds the plan was built with, in seconds.
type Settings struct {
	// MaxPause is the longest silence left between words; longer ones are cut down to the margins.
	MaxPause float64 `json:"max_pause"`
	// PadBefore and PadAfter are kept around words at every cut, so consonants are not clipped.
	PadBefore float64 `json:"pad_before"`
	PadAfter  float64 `json:"pad_after"`
	// RetakeSimilarity is the share of an earlier phrase's words (0..1) that must be said again, in order,
	// for it to count as a failed take.
	RetakeSimilarity float64 `json:"retake_similarity"`
	// Fillers are cut wherever they are, compared with repeated letters collapsed ("эээ" is "э").
	Fillers []string `json:"fillers"`
}

// Interval is a [Start, End) fragment of the source.
type Interval struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Cut is a removed fragment of the source with the reason and the words it held.
type Cut struct {
	Start  float64   `json:"start"`
	End    float64   `json:"end"`
	Reason CutReason `json:"reason"`
	Text   string    `json:"text,omitempty"`
}

// Take is one decision about a failed take: what was removed, which take was kept instead and why.
type Take struct {
	Kind TakeKind `json:"kind"`
	// Number of the removed take in its series of restarts and of the kept (last) take of the series.
	Number     int  `json:"number"`
	KeptNumber int  `json:"kept_number"`
	Removed    Span `json:"removed"`
	Kept       Span `json:"kept"`
	// Similarity is the share of the removed take's words said again in the kept one.
	Similarity  float64 `json:"similarity"`
	Explanation string  `json:"explanation"`
}

// Candidate is a phrase said again that was kept because the repeat may be intended. The editor accepts
// it by ID (CutData.AcceptCandidates, roughcut -accept): the plan is then built again with Earlier cut.
type Candidate struct {
	// ID is stable for the same transcript and settings: candidates are numbered in the order found.
	ID     int             `json:"id"`
	Reason CandidateReason `json:"reason"`
	// Earlier is the phrase that would be cut, Later the one said again.
	Earlier Span `json:"earlier"`
	Later   Span `json:"later"`
	// EarlierWords are the indices of Earlier's words in the source transcript, both included.
	EarlierWords WordRange `json:"earlier_words"`
	// Cut is the interval of the source accepting the candidate removes: with the margins around the
	// neighbouring words and on the frame grid, as the cut would be made.
	Cut         Interval `json:"cut"`
	Similarity  float64  `json:"similarity"`
	Explanation string   `json:"explanation"`
}

// WordRange is a range of indices into the transcript words, both included.
type WordRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// Span is a phrase of the source with its time.
type Span struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}
