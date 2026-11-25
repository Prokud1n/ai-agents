package main

import (
	"bytes"
	"errors"
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/editlocal"
	"github.com/Prokud1n/ai-agents/internal/tasks"
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
			args:     []string{"-in", "/in/clip.mov", "-out", "/out"},
			expected: options{in: "/in/clip.mov", out: "/out"},
		},
		{
			name:     "new edit with a caption",
			args:     []string{"-in", "/in/clip.mov", "-caption", "-5 кг за месяц", "-out", "/out"},
			expected: options{in: "/in/clip.mov", caption: "-5 кг за месяц", out: "/out"},
		},
		{
			name:     "revision",
			args:     []string{"-out", "/out", "-revision", "сделай 40 секунд"},
			expected: options{out: "/out", revision: "сделай 40 секунд"},
		},
		{name: "no out", args: []string{"-in", "/in/clip.mov"}, expectErr: true},
		{name: "nothing to do", args: []string{"-out", "/out"}, expectErr: true},
		{name: "blank revision", args: []string{"-out", "/out", "-revision", "  "}, expectErr: true},
		{name: "both", args: []string{"-in", "/in/clip.mov", "-out", "/out", "-revision", "короче"}, expectErr: true},
		{name: "caption of a revision", args: []string{"-out", "/out", "-revision", "короче", "-caption", "x"}, expectErr: true},
		{name: "extra argument", args: []string{"-in", "/in/clip.mov", "-out", "/out", "more"}, expectErr: true},
		{name: "unknown flag", args: []string{"-file", "/in/clip.mov"}, expectErr: true},
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
	assert.Equal(t, usageExitCode, run([]string{"-out", "/out"}, &stdout, &stderr))
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
			expected: []string{"Тренер получил бы: Не понял, что именно поменять.", "причина:"},
		},
		{name: "no worker", err: editlocal.ErrWorkerNotRunning, expected: []string{"media-worker не запущен: make up"}},
		{name: "no state", err: editlocal.ErrNoState, expected: []string{"в OUT нет state.json"}},
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
