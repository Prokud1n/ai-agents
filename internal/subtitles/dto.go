package subtitles

import (
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

type CreateSubtitlesData struct {
	// Transcript in the T3.1 format; after the rough cut (T3.2) its times are on the new timeline.
	Transcript *transcription.Result
	// Style overrides the service style (SUBTITLES_STYLE_FILE or the built-in one); nil keeps it.
	Style *Style
	// Accents are words or phrases to accent instead of the automatic choice (the Montage agent picks them).
	Accents []string
	// Karaoke highlights the word being spoken.
	Karaoke bool
	// Titles are the exercise titles («Выпады — 12 раз») on the timeline of the transcript, drawn in the Title style
	// apart from the speech subtitles. Their text is plain: it is escaped like speech.
	Titles []Title
	// Hook is the hook title drawn large in the Hook style over the first seconds (HookStyle.Duration, at most half a
	// second after the first phrase and the transcript's Duration); the subtitles of that time stand clear of it.
	// Plain text, escaped like speech; empty: none.
	Hook string
	// Notes are the points and numbers of a talking head (a list title, its items, short callouts) on the timeline of
	// the transcript, drawn in the Hook style at its place once the hook is gone. Plain text, escaped like speech.
	Notes []Note
	// Branding is the trainer's handle over the reel and the end card with it; nil or an empty handle: none.
	Branding *Branding
}

// NoteKind is what a screen note is.
type NoteKind string

const (
	// ListTitleNote starts a list: the items after it are shown under it.
	ListTitleNote NoteKind = "list_title"
	// ListItemNote is an item of the list: the items pile up line by line.
	ListItemNote NoteKind = "list_item"
	// CalloutNote is a short number or remark shown alone for a moment.
	CalloutNote NoteKind = "callout"
)

// Note is a point or a number shown from Start; End 0: a list stays until the next note, a callout a moment.
type Note struct {
	Kind  NoteKind
	Text  string
	Start float64
	End   float64
}

// Branding is the trainer's Instagram handle («@prokudina.ana»), drawn small at the right edge over the whole reel
// with a simple Instagram icon (Icon) and large in the middle of the end card over the last seconds (EndCard).
type Branding struct {
	Handle  string
	Icon    bool
	EndCard bool
}

// Title is a caption shown from Start to End, in seconds.
type Title struct {
	Text  string
	Start float64
	End   float64
}

type BurnSubtitlesData struct {
	CreateSubtitlesData

	VideoPath  string
	OutputPath string // MP4, H.264 + AAC, 1080×1920
	// Fit brings a video of another size to 1080×1920; empty means pad (nothing is cut off).
	Fit ffmpeg.FitMode
	// Cut, if set, renders only its fragments of the video in the same pass (the rough cut and the subtitles are
	// one encode); the transcript must then be on the cut's timeline.
	Cut *ffmpeg.Cut
	// ToneMap converts an HDR (HLG/PQ) video to SDR bt709 (ffmpeg.MediaInfo.HDR).
	ToneMap bool
}

// Subtitles is a rendered ASS document.
type Subtitles struct {
	ASS  []byte
	Cues int
}

type BurnResult struct {
	Cues int
	// FontFallbacks lists style fonts libass did not find and replaced ("Manrope -> /usr/share/.../Inter-Bold.otf"):
	// the video is rendered, but not in the brand fonts.
	FontFallbacks []string
	// MissingGlyphs are libass reports of characters absent in the fonts (e.g. emoji in the transcript).
	MissingGlyphs []string
	// Fonts are the fonts libass selected for the style families.
	Fonts []ffmpeg.FontSelection
	// Sound is what the render did to the loudness of a cut (the gain, its correction, the measured result); nil
	// without a cut or a loudness target.
	Sound *ffmpeg.SoundReport
}
