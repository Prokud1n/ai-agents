package ffmpeg

import (
	"bytes"
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowLevels(t *testing.T) {
	t.Parallel()

	pcm := func(values ...int16) []byte {
		var b bytes.Buffer
		require.NoError(t, binary.Write(&b, binary.LittleEndian, values))

		return b.Bytes()
	}

	repeat := func(v int16, n int) []int16 {
		out := make([]int16, n)
		for i := range out {
			out[i] = v
		}

		return out
	}

	tests := []struct {
		name     string
		input    []byte
		expected []float64
	}{
		{name: "nothing", input: nil, expected: nil},
		{
			name:     "full scale, then digital silence",
			input:    pcm(append(repeat(-32768, levelSamples), repeat(0, levelSamples)...)...),
			expected: []float64{0, minWindowLevel},
		},
		{
			name:     "half scale and a shorter last window, an odd byte ignored",
			input:    append(pcm(append(repeat(16384, levelSamples), repeat(-16384, 10)...)...), 7),
			expected: []float64{-6.0206, -6.0206},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := windowLevels(bytes.NewReader(tt.input))
			require.NoError(t, err)
			require.Len(t, got, len(tt.expected))

			for i := range got {
				assert.InDelta(t, tt.expected[i], got[i], 1e-3)
			}
		})
	}
}

// TestRunnerAudioLevels: a tone in [1, 2) s of a 3 s clip: the levels count
// from the first audio sample (the timeline of the transcript), loud in the tone and silent around it.
func TestRunnerAudioLevels(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	r := newTestRunner(time.Minute)
	src := filepath.Join(t.TempDir(), "tone.mkv")

	_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
		lavfiInput("aevalsrc=if(between(t\\,1\\,2)\\,0.5*sin(2*PI*440*t)\\,0):s=48000:d=3"),
		lavfiInput("testsrc=duration=3:size=64x64:rate=30"),
		[]string{videoCodecFlag, "ffv1", audioCodecFlag, pcmCodec, shortestFlag, src}), nil)
	require.NoError(t, err)

	levels, err := r.AudioLevels(ctx, src)
	require.NoError(t, err)
	assert.InDelta(t, LevelWindow, levels.Window, 1e-12)
	assert.InDelta(t, 300, len(levels.DB), 2)

	tone := 20 * math.Log10(0.5/math.Sqrt2)
	assert.InDelta(t, tone, levels.DB[150], 0.5, "the tone at 1.5 s")
	assert.Less(t, levels.DB[50], tone-60, "silence at 0.5 s")
	assert.Less(t, levels.DB[250], tone-60, "silence at 2.5 s")
	assert.Less(t, levels.DB[98], tone-60, "silence just before the tone")
	assert.InDelta(t, tone, levels.DB[101], 0.5, "the tone just after it starts")
}

func TestSoundGraph(t *testing.T) {
	t.Parallel()

	opts := RenderOptions{
		Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}, Offset: 0.1,
		Loudness: &LoudnessTarget{
			Integrated: -14, TruePeak: -2, Range: 11, Measured: &Loudness{Integrated: -24}, Correction: 1.5,
		},
	}

	// Far apart segments are still read from the one input: the sound alone is decoded whole.
	assert.Equal(t, "[0:a:0]asetpts=PTS-0.100000/TB,asplit=2[t0][t1];"+
		"[t0]atrim=start=1.000000:end=2.000000,asetpts=PTS-STARTPTS,"+
		"afade=t=in:st=0:d=0.010000,afade=t=out:st=0.990000:d=0.010000[a0];"+
		"[t1]atrim=start=30.000000:end=31.000000,asetpts=PTS-STARTPTS,"+
		"afade=t=in:st=0:d=0.010000,afade=t=out:st=0.990000:d=0.010000[a1];"+
		"[a0][a1]concat=n=2:v=0:a=1[j];[j]volume=11.5dB,aresample=192000,"+
		"alimiter=limit=0.794328:attack=5:release=50:level=false:latency=1,aresample=48000,"+
		"ebur128=peak=true:framelog=verbose[m]",
		soundGraph([]Segment{{Start: 1, End: 2}, {Start: 30, End: 31}}, opts))
}
