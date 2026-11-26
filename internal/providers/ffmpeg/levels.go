package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

// The level of the sound over time, for the cut points (roughcut): the first audio stream as mono 16 kHz PCM (the
// highs of the fricatives up to 8 kHz count), RMS in 10 ms windows. Decoding the sound alone takes about a second for
// a 4-minute source, the PCM is about 2 MB a minute in a temporary file.
const (
	// LevelWindow is the length of a window, seconds.
	LevelWindow    = 0.01
	levelSamples   = 160 // 16 kHz × LevelWindow
	levelFile      = "levels.pcm"
	sampleBytes    = 2
	fullScale      = 32768.0
	minWindowLevel = -120.0 // digital silence, dBFS
	decibels       = 20.0
	readBuffer     = 64 << 10
	byteBits       = 8
	signBit        = 1 << 15 // of a 16-bit sample
	wordRange      = 1 << 16
)

// Levels is the RMS level of the sound in dBFS, one value per Window seconds from the first audio sample (the timeline
// of the transcript).
type Levels struct {
	Window float64
	DB     []float64
}

// AudioLevels measures the level of the first audio stream of src in LevelWindow windows.
func (r *Runner) AudioLevels(ctx context.Context, src string) (*Levels, error) {
	in, err := localFile(src)
	if err != nil {
		return nil, err
	}

	dir, err := os.MkdirTemp("", filterDirPattern)
	if err != nil {
		return nil, fmt.Errorf("failed to create levels dir: %w", err)
	}

	defer func() { _ = os.RemoveAll(dir) }() // best effort: the PCM is in the temp dir

	pcm := filepath.Join(dir, levelFile)

	out, err := localFile(pcm)
	if err != nil {
		return nil, err
	}

	args := append(commonArgs(), inputArgs(in)...)
	args = append(args, mapFlag, "0:a:0", channelsFlag, audioChannels, sampleRateFlag, audioSampleRate,
		formatFlag, "s16le", out)

	if _, err := r.run(ctx, r.ffmpegPath, args, nil); err != nil {
		return nil, fmt.Errorf("failed to decode the sound for its levels: %w", err)
	}

	f, err := os.Open(filepath.Clean(pcm))
	if err != nil {
		return nil, fmt.Errorf("failed to open the sound levels: %w", err)
	}
	defer f.Close()

	db, err := windowLevels(bufio.NewReaderSize(f, readBuffer))
	if err != nil {
		return nil, err
	}

	return &Levels{Window: LevelWindow, DB: db}, nil
}

// windowLevels reads little-endian 16-bit mono PCM and returns the RMS level of every whole window of levelSamples
// (and of the last, shorter one), dBFS.
func windowLevels(r io.Reader) ([]float64, error) {
	var (
		db     []float64
		sample [sampleBytes]byte
		sum    float64
		n      int
	)

	for {
		if _, err := io.ReadFull(r, sample[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}

			return nil, fmt.Errorf("failed to read the sound levels: %w", err)
		}

		u := int(sample[0]) | int(sample[1])<<byteBits
		if u >= signBit {
			u -= wordRange
		}

		v := float64(u) / fullScale
		sum += v * v
		n++

		if n == levelSamples {
			db = append(db, rmsLevel(sum, n))
			sum, n = 0, 0
		}
	}

	if n > 0 {
		db = append(db, rmsLevel(sum, n))
	}

	return db, nil
}

// rmsLevel is the level of n samples whose squares sum to sum, dBFS, not below minWindowLevel.
func rmsLevel(sum float64, n int) float64 {
	if sum <= 0 {
		return minWindowLevel
	}

	return max(decibels*math.Log10(math.Sqrt(sum/float64(n))), minWindowLevel)
}
