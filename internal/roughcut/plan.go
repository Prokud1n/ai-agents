package roughcut

// PlanVersion is the version of the plan.json format; it changes when a field changes its meaning.
const PlanVersion = 1

// Mode is how the cut treats the silence between words.
type Mode string

const (
	// TalkMode is a talking-head reel: pauses, fillers and failed takes are cut. An empty mode is TalkMode.
	TalkMode Mode = "talk"
	// WorkoutMode is a workout reel: the exercises are shown, often in silence, so nothing is cut automatically —
	// only what the editor cut explicitly (Edit.Remove, Edit.CutTime, accepted candidates).
	WorkoutMode Mode = "workout"
)

// Effective is the mode with the empty one read as TalkMode.
func (m Mode) Effective() Mode {
	if m == WorkoutMode {
		return WorkoutMode
	}

	return TalkMode
}

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
	// EditCutReason is a fragment the editor asked to cut (Edit.Remove, Edit.CutTime): a revision of the trainer.
	EditCutReason CutReason = "edit"
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
	// Edit is what the editor changed on top of the automatic plan (CutData.Edit): the next revision starts
	// from it. Nil for an automatic plan.
	Edit *Edit `json:"edit,omitempty"`
	// Mode the plan was built in (Edit.Mode); empty for a talking-head plan made before modes existed.
	Mode Mode `json:"mode,omitempty"`
	// SpeechShare is the share of the source (0..1) the automatic talking-head cut keeps — the speech with its short
	// pauses — whatever the mode and the edit: the media worker plans a source with little speech as a workout by it,
	// and the editor's model sees it.
	SpeechShare float64 `json:"speech_share"`
	// MinSpeechShare is the threshold the media worker compared SpeechShare with (MEDIA_WORKER_MIN_SPEECH_SHARE;
	// CutData.MinSpeechShare); 0 when unknown or off.
	MinSpeechShare float64 `json:"min_speech_share,omitempty"`
	// SoundCuts: the cut points of the talking head were placed by the level of the source sound (the speech found
	// around the words, the pauses cut at their quietest frame down to a breath); false: by the margins around the words.
	SoundCuts bool `json:"sound_cuts,omitempty"`
	// LongPauses counts the pauses longer than 0.3 s left inside the reel: by the sound level when it is known, by the
	// gaps between the words otherwise (Service.Cut).
	LongPauses int `json:"long_pauses"`
	// SoundLevels are the levels of the source sound the cut points were placed by; nil when they were not read.
	SoundLevels *SoundLevels `json:"sound_levels,omitempty"`
}

// SoundLevels are the levels of the source sound (dBFS): the noise floor (the 10th percentile of the 10 ms windows),
// the loud speech (the 95th) and the threshold of speech between them. Used is false when the two are too close to
// tell speech from silence: the margins around the words were used. They calibrate the cut on real recordings.
type SoundLevels struct {
	NoiseFloor float64 `json:"noise_floor"`
	Speech     float64 `json:"speech"`
	Threshold  float64 `json:"threshold"`
	Used       bool    `json:"used"`
}

// Edit is what the editor (the editing agent, by the trainer's caption or revision) changed on top of the
// automatic plan. The plan is built again from the transcript with the same settings and rules
// (BuildPlanEdited), so the same edit always gives the same plan, and the IDs of the candidates do not depend
// on it. Word indices are into the source transcript; ranges include both ends.
type Edit struct {
	// Accept are IDs of Plan.Candidates whose earlier phrase is cut.
	Accept []int `json:"accept,omitempty"`
	// Remove are words cut in addition to the automatic cuts; Restore are words kept although the automatic
	// plan (a filler, a failed take, an accepted candidate) cut them. A word in both is cut.
	Remove  []WordRange `json:"remove,omitempty"`
	Restore []WordRange `json:"restore,omitempty"`
	// Words replace the text of transcript words in the subtitles only: the plan is built from the original
	// words.
	Words []WordFix `json:"words,omitempty"`
	// Notes explain the editor's decisions of this version in the decisions log; Details are the lines around them
	// that are not decisions (the length of a reel shortened by the selection, the phrases left out), not counted.
	Notes   []string `json:"notes,omitempty"`
	Details []string `json:"details,omitempty"`
	// Mode of the cut; empty is TalkMode.
	Mode Mode `json:"mode,omitempty"`
	// CutTime are fragments of the source cut by time, in seconds of the source (a trim of a workout, «вырежи
	// с 0:15 до 0:20»), in both modes.
	CutTime []Interval `json:"cut_time,omitempty"`
	// Titles are the exercise titles drawn over the reel, on the source timeline.
	Titles []Title `json:"titles,omitempty"`
	// Mute silences the sound of the source (the trainer adds a track in Instagram); the speech subtitles go too.
	Mute bool `json:"mute,omitempty"`
	// Hide are words left out of the subtitles only, the video and the sound stay (a phrase the speech recognition
	// made up on music, which cutting would take seconds of an exercise with it). Indices of the source transcript.
	Hide []WordRange `json:"hide,omitempty"`
	// Hook is the hook title drawn large over the first seconds of the reel; empty: none. Its text is checked by the
	// app like the exercise titles (no line breaks, invisible characters or markup).
	Hook string `json:"hook,omitempty"`
	// ScreenNotes are the points and numbers drawn at the top of a talking head after the hook (a list title, its
	// items that pile up line by line, short callouts), on the source timeline; checked by the app like the hook.
	ScreenNotes []ScreenNote `json:"screen_notes,omitempty"`
}

// NoteKind is what a screen note is.
type NoteKind string

const (
	// ListTitleNote starts a list («3 вещи»): the items after it are shown under it.
	ListTitleNote NoteKind = "list_title"
	// ListItemNote is an item of the list («1/ на русском»): the items pile up line by line.
	ListItemNote NoteKind = "list_item"
	// CalloutNote is a short number or remark («6 лет») shown alone for a moment.
	CalloutNote NoteKind = "callout"
	// SectionNote is a section heading of a talking head («страх 1»): shown above the subtitles while the section
	// starts, the next one replaces it.
	SectionNote NoteKind = "section"
)

// ScreenNote is a point or a number on the screen from Start, in seconds of the source; End 0 lets the subtitles
// choose (a list stays until the next note, a callout a moment).
type ScreenNote struct {
	Kind  NoteKind `json:"kind"`
	Text  string   `json:"text"`
	Start float64  `json:"start"`
	End   float64  `json:"end,omitempty"`
}

// Title is an exercise title («Выпады — 12 раз») shown from Start to End, in seconds of the source. End 0 means until
// the next title or the end of the reel. Its text is checked by the app (no line breaks, invisible characters or
// markup).
type Title struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end,omitempty"`
}

// WordFix is a corrected subtitle word: Text replaces the word Index of the source transcript.
type WordFix struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
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
	// EarlierWords and LaterWords are the indices of the phrases' words in the source transcript, both included.
	EarlierWords WordRange `json:"earlier_words"`
	LaterWords   WordRange `json:"later_words"`
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
