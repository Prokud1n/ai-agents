package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Output video for Instagram Reels: H.264 High (codec, CRF and pixel format as the rough cut render), AAC 48 kHz,
// moov atom at the start. CRF keeps the quality even; the bitrate is capped (VBV) so a busy frame cannot blow the file
// up: 10 Mbit/s is well above what a talking head needs at CRF 20 (4-8 Mbit/s at 1080×1920) and what Instagram keeps
// after its own encode.
const (
	// publishPreset: the draft is published to Instagram, which encodes it again for its own bitrate (a few Mbit/s
	// at 1080×1920); veryfast at CRF 20 keeps more than that survives, and encodes about twice as fast as medium.
	publishPreset = videoPreset // the same as the rough cut render
	videoProfile  = "high"
	outAudioRate  = "48000"
	outAudioBits  = "192k"
	maxVideoRate  = "10M"
	videoBuffer   = "20M"
	outChannels   = "2" // mono or multichannel phone audio becomes stereo
	// The export is always tagged SDR bt709, limited range: an HDR source is tone mapped to it (ToneMap), an SDR one
	// is converted to the bt709 matrix by scale.
	bt709 = "bt709"
	// toneMapFilter brings HLG/PQ (bt2020) to SDR bt709: linear light, primaries to bt709, the Hable curve, back to
	// the bt709 transfer and matrix, 8-bit 4:2:0. zscale (zimg) reads the input transfer and primaries from the frames.
	toneMapFilter = "zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=hable:desat=0," +
		"zscale=t=bt709:m=bt709:r=tv,format=yuv420p"
	// colorTagsFilter tags the frames SDR bt709 at the end of the graph: ffmpeg 8+ takes the stream's color tags from
	// the frames and no longer writes -color_primaries/-color_trc of the output (those stay for older versions).
	colorTagsFilter = "setparams=color_primaries=" + bt709 + ":color_trc=" + bt709 + ":colorspace=" + bt709 + ":range=tv"

	cutVideoLabel = "cut"
	cutAudioLabel = "a"

	mapFlag        = "-map"
	formatFlag     = "-f"
	videoCodecFlag = "-c:v"

	videoLabel = "v"
	maxAlpha   = 255

	fontSelectMarker = "fontselect:"
	glyphMarker      = "Glyph 0x" // libass: a glyph is missing in the requested font
	fontFailedMarker = "failed to find"
	maxFontLines     = 1000
)

// FitMode says how a frame of another size or aspect is brought to the output size.
type FitMode string

const (
	// PadFitMode scales the whole frame into the output and fills the rest with black: nothing is cut off.
	PadFitMode FitMode = "pad"
	// CropFitMode scales the frame to cover the output and cuts the overflow at the edges.
	CropFitMode FitMode = "crop"
)

var (
	ErrUnsafeName     = errors.New("name must be a plain file name of letters, digits, '.', '_' or '-'")
	ErrInvalidFitMode = errors.New("unknown fit mode")
	ErrInvalidSize    = errors.New("output size must be positive and even")
	ErrInvalidShade   = errors.New("shade must have 0 <= plateau < height <= output height and alpha 0..255")
	ErrNoWorkDir      = errors.New("a render with a cut needs the working directory")

	safeNameRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
	fontSelectRe = regexp.MustCompile(`fontselect: \((.*), (\d+), (\d+)\) -> (.*), (-?\d+), (.*)$`)
)

// BurnSubtitlesData describes one render. The ASS file and the fonts directory are given as plain names inside
// Dir: ffmpeg runs in Dir, so no path reaches the filter graph and nothing in it needs escaping.
type BurnSubtitlesData struct {
	Input  string // any video ffmpeg reads; local files only
	Output string // MP4
	Dir    string
	// SubtitlesName and FontsName are plain names of the ASS file and the fonts directory in Dir.
	SubtitlesName string
	FontsName     string
	Width         int
	Height        int
	Fit           FitMode
	// Shade darkens the frame under the subtitles; nil means none.
	Shade *Shade
	// ToneMap converts an HDR (HLG/PQ) picture to SDR bt709 after scaling it to the output size (MediaInfo.HDR).
	ToneMap bool
	// Cut, if set, keeps only its fragments of the input, one after another, in the same pass: the video is
	// decoded and encoded once, the rough cut, the subtitles and the export are one ffmpeg run. The subtitles must
	// then be on the timeline of the cut (roughcut.RemapTranscript). It needs Dir: the graph is written there.
	Cut *Cut
}

// Cut is the rough cut of a render: the kept fragments and how they are cut (see RenderSegments).
type Cut struct {
	Segments []Segment
	Options  RenderOptions
	// Zoom, if set, changes the framing at the joints (a talking head); it works on the output frame, after the fit
	// and the tone mapping, and under the shade and the subtitles, which stay where they are.
	Zoom *Zoom
}

// Shade darkens the frame from its top (or bottom) edge: an even Alpha (0..255) over Plateau pixels — where the
// subtitles stand — then a cosine fade to nothing at Height pixels, so it has no visible border.
type Shade struct {
	Height  int
	Plateau int
	Alpha   int
	Bottom  bool
}

// FontSelection is a font libass picked for a requested family, as it reports in the ffmpeg log:
// "fontselect: (Manrope, 700, 0) -> Manrope-Bold, 0, Manrope-Bold". Font is a file path for system fonts
// and the font name for fonts loaded from fontsdir.
type FontSelection struct {
	Family         string
	Weight         int
	Italic         int
	Font           string
	PostScriptName string
}

// BurnReport is what libass said about fonts during the render.
type BurnReport struct {
	Fonts []FontSelection
	// MissingGlyphs are libass lines about characters absent in the requested font (e.g. emoji).
	MissingGlyphs []string
	// Sound is what the render did to the loudness of a cut; nil without a cut or a loudness target.
	Sound *SoundReport
}

// BurnSubtitles renders the ASS subtitles onto the video, bringing it to Width×Height, and encodes H.264 + AAC.
func (r *Runner) BurnSubtitles(ctx context.Context, data *BurnSubtitlesData) (*BurnReport, error) {
	in, out, err := localFiles(data.Input, data.Output)
	if err != nil {
		return nil, err
	}

	major := unknownMajor
	burn, passes := data, 0

	var fitErr error

	if data.Cut != nil {
		if major, err = r.majorVersion(ctx); err != nil {
			return nil, err
		}

		burn, passes, fitErr = r.fitCutLoudness(ctx, in, data)
		if fitErr != nil {
			if ctx.Err() != nil {
				return nil, fitErr
			}

			// The render goes on with the gain by the measurement of the source; the report tells why.
			burn, passes = data, 0
		}
	}

	args, graph, err := burnSubtitlesArgs(in, out, burn, major)
	if err != nil {
		return nil, fmt.Errorf("failed to burn subtitles: %w", err)
	}

	if data.Cut != nil {
		// Hundreds of fragments do not fit one command-line argument: the graph goes through a file in Dir.
		if err := os.WriteFile(filepath.Join(data.Dir, filterFileName), []byte(graph), filterFileMode); err != nil {
			return nil, fmt.Errorf("failed to write filter graph: %w", err)
		}
	}

	fonts := &lineCollector{marker: fontSelectMarker, maxLines: maxFontLines, maxLine: maxLogLine}
	glyphs := &lineCollector{marker: glyphMarker, maxLines: maxFontLines, maxLine: maxLogLine}
	if _, err := r.runIn(ctx, data.Dir, r.ffmpegPath, args, io.MultiWriter(fonts, glyphs)); err != nil {
		return nil, fmt.Errorf("failed to burn subtitles: %w", err)
	}

	missing := nonEmptyLines(glyphs.String())
	for _, line := range nonEmptyLines(fonts.String()) {
		if strings.Contains(line, fontFailedMarker) {
			missing = append(missing, line)
		}
	}

	report := &BurnReport{Fonts: parseFontSelections(fonts.String()), MissingGlyphs: missing}
	if report.Sound = soundReport(burn.Cut, passes, fitErr); report.Sound != nil {
		report.Sound.Output = r.measureFile(ctx, out)
		if err := r.capFilePeak(ctx, data.Output, report.Sound); err != nil {
			return nil, err
		}
	}

	return report, nil
}

// fitCutLoudness is the data with the gain of the cut corrected (Runner.fitLoudness) and the number of passes it took.
func (r *Runner) fitCutLoudness(ctx context.Context, in string, data *BurnSubtitlesData) (*BurnSubtitlesData, int, error) {
	opts, passes, err := r.fitLoudness(ctx, in, data.Cut.Segments, data.Cut.Options)
	if err != nil {
		return nil, 0, err
	}

	cut := *data.Cut
	cut.Options = opts
	out := *data
	out.Cut = &cut

	return &out, passes, nil
}

// soundReport is the loudness the render of the cut applied; nil without a cut or a loudness target.
func soundReport(cut *Cut, passes int, fitErr error) *SoundReport {
	if cut == nil || cut.Options.Mute || cut.Options.Loudness == nil {
		return nil
	}

	l := cut.Options.Loudness

	return &SoundReport{Gain: l.Gain(), Correction: l.Correction, Passes: passes, FitError: fitErr}
}

// burnSubtitlesArgs returns the arguments and the filter graph. Without a cut the graph is an argument; with one
// it is read from the file filterFileName in Dir (ffmpeg runs there), so only our own plain name is on the command
// line.
func burnSubtitlesArgs(input, output string, data *BurnSubtitlesData, ffmpegMajor int) (args []string, graph string, err error) {
	graph, err = burnGraph(data)
	if err != nil {
		return nil, "", err
	}

	args = commonArgs()
	// libass reports the chosen fonts ("fontselect") at the info level: the fallback check depends on it.
	args = append(args, "-loglevel", "info")

	if data.Cut != nil {
		args = append(args, cutInputArgs(input, groupSegments(data.Cut.Segments, data.Cut.Options))...)
	} else {
		args = append(args, inputArgs(input)...)
	}

	// The first video stream is required; without a cut audio is encoded when there is one ("?"), a cut has it.
	audio := "0:a:0?"

	if data.Cut != nil {
		if data.Dir == "" {
			return nil, "", ErrNoWorkDir
		}

		args = append(args, filterFileArgs(filterFileName, ffmpegMajor)...)
		audio = "[" + cutAudioLabel + "]"
	} else {
		args = append(args, "-filter_complex", graph)
	}

	args = append(args,
		mapFlag, "["+videoLabel+"]", mapFlag, audio,
		videoCodecFlag, videoCodec, "-preset", publishPreset, "-crf", videoCRF,
		"-maxrate", maxVideoRate, "-bufsize", videoBuffer,
		"-profile:v", videoProfile, "-pix_fmt", videoPixFmt,
		"-colorspace", bt709, "-color_primaries", bt709, "-color_trc", bt709, "-color_range", "tv",
		audioCodecFlag, renderACodec, "-b:a", outAudioBits, "-ar", outAudioRate, "-ac", outChannels,
		"-movflags", "+faststart", formatFlag, renderFormat,
	)

	return append(args, output), graph, nil
}

// burnGraph scales the frame to the output size (pad or crop) first, tone maps an HDR picture on that small frame,
// lays the shade over it, then draws the subtitles with libass: every filter after the scale works on 1080×1920.
// The shade is one generated frame: overlay repeats it over the whole video. With a cut the fragments are cut and
// joined first (segmentsGraph, at the draft's frame rate), and the joined picture is what is scaled.
func burnGraph(data *BurnSubtitlesData) (string, error) {
	input, cut := "[0:v]", ""

	if data.Cut != nil {
		if len(data.Cut.Segments) == 0 {
			return "", fmt.Errorf("failed to cut: %w", errNoSegments)
		}

		input = "[" + cutVideoLabel + "]"
		cut = segmentsGraph(data.Cut.Segments, data.Cut.Options, cutVideoLabel, cutAudioLabel) + ";"
	}

	frame, err := frameGraph(data, input)
	if err != nil {
		return "", err
	}

	return cut + frame, nil
}

// frameGraph brings the picture of the input label to the output frame and draws the subtitles on it.
func frameGraph(data *BurnSubtitlesData, input string) (string, error) {
	if !safeNameRe.MatchString(data.SubtitlesName) || !safeNameRe.MatchString(data.FontsName) {
		return "", fmt.Errorf("subtitles %q, fonts %q: %w", data.SubtitlesName, data.FontsName, ErrUnsafeName)
	}

	if data.Width <= 0 || data.Height <= 0 || data.Width%2 != 0 || data.Height%2 != 0 {
		return "", fmt.Errorf("%dx%d: %w", data.Width, data.Height, ErrInvalidSize)
	}

	fit, err := fitFilter(data)
	if err != nil {
		return "", err
	}

	if data.Cut != nil && data.Cut.Zoom != nil {
		zoom, err := zoomGraph(data.Cut, data.Width, data.Height)
		if err != nil {
			return "", err
		}

		fit += zoom
	}

	subtitles := "ass=filename=" + data.SubtitlesName + ":fontsdir=" + data.FontsName + "," + colorTagsFilter +
		"[" + videoLabel + "]"

	if data.Shade == nil {
		return input + fit + ",setsar=1," + subtitles, nil
	}

	shade, y, err := shadeSource(data.Shade, data.Width, data.Height)
	if err != nil {
		return "", err
	}

	return input + fit + ",setsar=1[base];" + shade + "[shade];" +
		"[base][shade]overlay=x=0:y=" + y + "," + subtitles, nil
}

// fitFilter brings the frame to the output size (pad or crop). An SDR picture is converted to the bt709 matrix by scale
// (an SD source is bt601). An HDR one is scaled as it is (10-bit HLG/PQ, its matrix and range) and tone mapped after
// the scaling: zimg in float32 is the most expensive filter of the graph, and on a 1080×1920 frame it has a quarter of
// the pixels of a 4K source to convert. The black of pad is the same black in either range.
func fitFilter(data *BurnSubtitlesData) (string, error) {
	w, h := strconv.Itoa(data.Width), strconv.Itoa(data.Height)

	matrix := ":out_color_matrix=" + bt709 + ":out_range=tv"
	if data.ToneMap {
		matrix = ""
	}

	var fit string

	switch data.Fit {
	case PadFitMode:
		fit = "scale=" + w + ":" + h + ":force_original_aspect_ratio=decrease:force_divisible_by=2" + matrix + "," +
			"pad=" + w + ":" + h + ":(ow-iw)/2:(oh-ih)/2:color=black"
	case CropFitMode:
		fit = "scale=" + w + ":" + h + ":force_original_aspect_ratio=increase:force_divisible_by=2" + matrix + "," +
			"crop=" + w + ":" + h
	default:
		return "", fmt.Errorf("%q: %w", data.Fit, ErrInvalidFitMode)
	}

	if data.ToneMap {
		fit += "," + toneMapFilter
	}

	return fit, nil
}

// shadeSource generates the gradient frame and returns it with its y position. The expression is built from
// numbers only and quoted, so its commas stay inside geq.
func shadeSource(s *Shade, width, height int) (source, y string, err error) {
	if s.Plateau < 0 || s.Plateau >= s.Height || s.Height > height || s.Alpha < 0 || s.Alpha > maxAlpha {
		return "", "", fmt.Errorf("height %d, plateau %d, alpha %d: %w", s.Height, s.Plateau, s.Alpha, ErrInvalidShade)
	}

	h, p, fade := strconv.Itoa(s.Height), strconv.Itoa(s.Plateau), strconv.Itoa(s.Height-s.Plateau)
	// distance from the edge where the shade starts
	dist, y := "Y", "0"

	if s.Bottom {
		dist, y = "("+h+"-1-Y)", strconv.Itoa(height-s.Height)
	}

	curve := "if(lt(" + dist + "," + p + "),1,(1+cos(PI*(" + dist + "-" + p + ")/" + fade + "))/2)"
	source = "color=c=black:s=" + strconv.Itoa(width) + "x" + h + ":r=1:d=1,format=rgba," +
		"geq=r=0:g=0:b=0:a='" + strconv.Itoa(s.Alpha) + "*" + curve + "'"

	return source, y, nil
}

// parseFontSelections reads "fontselect: (family, weight, italic) -> font, index, postscript name" lines.
func parseFontSelections(log string) []FontSelection {
	selections := []FontSelection{}

	for line := range strings.SplitSeq(log, "\n") {
		m := fontSelectRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}

		weight, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}

		italic, err := strconv.Atoi(m[3])
		if err != nil {
			continue
		}

		selections = append(selections, FontSelection{
			Family: m[1], Weight: weight, Italic: italic, Font: m[4], PostScriptName: strings.TrimSpace(m[6]),
		})
	}

	return selections
}

func nonEmptyLines(s string) []string {
	lines := []string{}

	for line := range strings.SplitSeq(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}

	return lines
}
