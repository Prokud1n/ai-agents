package ffmpeg

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	videoCodecType = "video"
	audioCodecType = "audio"
	probeLimit     = 64 << 10 // ffprobe JSON of a few streams
)

// MediaInfo is what the rough cut needs to know about a source file. Times are in seconds.
type MediaInfo struct {
	Duration float64
	HasVideo bool
	HasAudio bool
	// AvgFrameRate and RealFrameRate of the first video stream (avg_frame_rate, r_frame_rate); zero if unknown.
	AvgFrameRate  Rational
	RealFrameRate Rational
	// FormatStart is the start_time of the file; AudioStart and VideoStart of its first audio and video
	// streams. ffmpeg subtracts FormatStart from all timestamps it reads, so inside a filter graph the
	// first audio sample is at AudioStart - FormatStart.
	FormatStart float64
	AudioStart  float64
	VideoStart  float64
	// ColorTransfer and ColorPrimaries of the first video stream as ffprobe names them ("arib-std-b67", "bt2020");
	// empty if not tagged.
	ColorTransfer  string
	ColorPrimaries string
	// Width, Height and PixelFormat ("yuv420p10le") of the first video stream as stored (before a rotation); zero if
	// unknown.
	Width       int
	Height      int
	PixelFormat string
}

// highDepthMarks are in the names of the pixel formats with more than 8 bits a sample: yuv420p10le, p010le, yuv422p12.
var highDepthMarks = []string{"p10", "p12", "p14", "p16", "010", "012", "016"}

// Frame is the decoded frame of the first video stream; nil if its size is unknown.
func (m *MediaInfo) Frame() *SourceFrame {
	if m.Width <= 0 || m.Height <= 0 {
		return nil
	}

	bytes := 1
	for _, mark := range highDepthMarks {
		if strings.Contains(m.PixelFormat, mark) {
			bytes = 2
		}
	}

	return &SourceFrame{Width: m.Width, Height: m.Height, BytesPerSample: bytes}
}

// HDR transfer functions of phones: HLG (iPhone, Android) and PQ (HDR10, Dolby Vision base layer).
const (
	hlgTransfer = "arib-std-b67"
	pqTransfer  = "smpte2084"
)

// HDR reports whether the video is HDR (HLG or PQ): it must be tone mapped to SDR bt709 for an 8-bit export, or its
// colours come out washed out.
func (m *MediaInfo) HDR() bool {
	return m.ColorTransfer == hlgTransfer || m.ColorTransfer == pqTransfer
}

// Rational is a frame rate as ffprobe gives it: 30000/1001. The zero value is unknown.
type Rational struct {
	Num int64
	Den int64
}

// Float returns the rate in frames per second, 0 if unknown.
func (r Rational) Float() float64 {
	if r.Num <= 0 || r.Den <= 0 {
		return 0
	}

	return float64(r.Num) / float64(r.Den)
}

// String formats the rate for ffmpeg options: "30000/1001".
func (r Rational) String() string {
	return strconv.FormatInt(r.Num, decimalBase) + frameRateDivider + strconv.FormatInt(r.Den, decimalBase)
}

type probeOutput struct {
	Streams []struct {
		CodecType      string `json:"codec_type"`
		AvgFrameRate   string `json:"avg_frame_rate"`
		RFrameRate     string `json:"r_frame_rate"`
		StartTime      string `json:"start_time"`
		ColorTransfer  string `json:"color_transfer"`
		ColorPrimaries string `json:"color_primaries"`
		Width          int    `json:"width"`
		Height         int    `json:"height"`
		PixelFormat    string `json:"pix_fmt"`
	} `json:"streams"`
	Format struct {
		Duration  string `json:"duration"`
		StartTime string `json:"start_time"`
	} `json:"format"`
}

// Probe reads the streams and the duration of a media file with ffprobe.
func (r *Runner) Probe(ctx context.Context, path string) (MediaInfo, error) {
	in, err := localFile(path)
	if err != nil {
		return MediaInfo{}, err
	}

	stdout, err := r.runLimited(ctx, r.ffprobePath, probeArgs(in), probeLimit)
	if err != nil {
		return MediaInfo{}, fmt.Errorf("failed to probe media: %w", err)
	}

	info, err := parseProbe(stdout)
	if err != nil {
		return MediaInfo{}, fmt.Errorf("failed to parse probe: %w", err)
	}

	return info, nil
}

func probeArgs(input string) []string {
	return probeEntriesArgs(input, "stream=codec_type,avg_frame_rate,r_frame_rate,start_time,color_transfer,color_primaries,width,height,pix_fmt:"+
		"format=duration,start_time", "json")
}

func parseProbe(s string) (MediaInfo, error) {
	var out probeOutput
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return MediaInfo{}, fmt.Errorf("failed to decode ffprobe json: %w", err)
	}

	var info MediaInfo

	duration, err := parseDuration(out.Format.Duration)
	if err != nil {
		return MediaInfo{}, err
	}

	info.Duration = duration
	info.FormatStart = parseStart(out.Format.StartTime)

	for i := range out.Streams {
		st := &out.Streams[i]

		switch {
		case st.CodecType == videoCodecType && !info.HasVideo:
			info.HasVideo = true
			info.VideoStart = parseStart(st.StartTime)
			info.ColorTransfer, info.ColorPrimaries = st.ColorTransfer, st.ColorPrimaries
			info.Width, info.Height, info.PixelFormat = st.Width, st.Height, st.PixelFormat

			if info.AvgFrameRate, err = parseRational(st.AvgFrameRate); err != nil {
				return MediaInfo{}, err
			}

			if info.RealFrameRate, err = parseRational(st.RFrameRate); err != nil {
				return MediaInfo{}, err
			}
		case st.CodecType == audioCodecType && !info.HasAudio:
			info.HasAudio = true
			info.AudioStart = parseStart(st.StartTime)
		}
	}

	return info, nil
}

// parseStart reads start_time; "N/A" or a missing value is 0.
func parseStart(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), float64Bits)
	if err != nil {
		return 0
	}

	return v
}
