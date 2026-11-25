package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/editlocal"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	inFlag       = "-in"
	outFlag      = "-out"
	revisionFlag = "-revision"
	captionFlag  = "-caption"
	testClip     = "/in/clip.mov"
	testOut      = "/out"
)

func TestParseFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		expected  options
		expectErr bool
	}{
		{
			name:     "new edit",
			args:     []string{inFlag, testClip, outFlag, testOut},
			expected: options{in: testClip, out: testOut},
		},
		{
			name:     "new edit with a caption",
			args:     []string{inFlag, testClip, captionFlag, "-5 кг за месяц", outFlag, testOut},
			expected: options{in: testClip, caption: "-5 кг за месяц", out: testOut},
		},
		{
			name:     "revision",
			args:     []string{outFlag, testOut, revisionFlag, "сделай 40 секунд"},
			expected: options{out: testOut, revision: "сделай 40 секунд"},
		},
		{name: "no out", args: []string{inFlag, testClip}, expectErr: true},
		{name: "download what is left", args: []string{outFlag, testOut}, expected: options{out: testOut}},
		{name: "caption without a source", args: []string{outFlag, testOut, captionFlag, "x"}, expectErr: true},
		{name: "blank revision", args: []string{outFlag, testOut, revisionFlag, "  "}, expectErr: true},
		{name: "both", args: []string{inFlag, testClip, outFlag, testOut, revisionFlag, "короче"}, expectErr: true},
		{name: "caption of a revision", args: []string{outFlag, testOut, revisionFlag, "короче", captionFlag, "x"}, expectErr: true},
		{name: "extra argument", args: []string{inFlag, testClip, outFlag, testOut, "more"}, expectErr: true},
		{name: "unknown flag", args: []string{"-file", testClip}, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts, err := parseFlags(tt.args, &bytes.Buffer{})
			if tt.expectErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, opts)
		})
	}
}

func TestHelpExitsZero(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	_, err := parseFlags([]string{"-h"}, &stderr)
	require.ErrorIs(t, err, flag.ErrHelp)

	assert.Equal(t, 0, run([]string{"-h"}, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "usage:")
	assert.Equal(t, usageExitCode, run([]string{outFlag, testOut}, &stdout, &stderr))
}

func TestPrintFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected []string
	}{
		{
			name: "refused revision",
			err: errors.Join(errors.New("failed to make the revision"),
				&tasks.PermanentError{Message: "Не понял, что именно поменять.", Err: errors.New("unclear")}),
			expected: []string{"Тренер получил бы: Не понял, что именно поменять.", "причина: unclear\n"},
		},
		{name: "no worker", err: editlocal.ErrWorkerNotRunning, expected: []string{"media-worker не запущен: make up"}},
		{name: "no state", err: editlocal.ErrNoState, expected: []string{"в OUT нет state.json"}},
		{name: "stale lock", err: editlocal.ErrStaleLock, expected: []string{"удалите OUT/.lock"}},
		{name: "source gone", err: editlocal.ErrSourceMissing, expected: []string{"исходник не найден в S3"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			printFailure(&out, tt.err)

			for _, s := range tt.expected {
				assert.Contains(t, out.String(), s)
			}
		})
	}
}

func TestPrintReport(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	printReport(&out, &editlocal.Report{
		TaskID: editlocal.TaskIDBase + 1, Version: 2, Done: true, Dir: "v2",
		Output: tasks.Output{Text: "Новая версия рилс"}, Usage: editlocal.Usage{Calls: 1},
	})

	assert.Contains(t, out.String(), "Версия 2 готова: OUT/v2/")
	assert.Contains(t, out.String(), "Задача: 1000000000000000001")
	assert.Contains(t, out.String(), "вызовов API 1")
	assert.Contains(t, out.String(), "Новая версия рилс")
}

func TestPrintFailureOnce(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	printFailure(&out, &tasks.PermanentError{Message: "Текст тренеру.", Err: errors.New("cause")})

	assert.Equal(t, 1, strings.Count(out.String(), "Текст тренеру."))
}
