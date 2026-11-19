package pgtest

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIsStale(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	name := func(created time.Time) string {
		return databasePrefix + strconv.FormatInt(created.Unix(), 10) + "_123_1"
	}

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "older than an hour", input: name(now.Add(-2 * time.Hour)), expected: true},
		{name: "fresh", input: name(now.Add(-time.Minute)), expected: false},
		{name: "not a pgtest name", input: "pgtest_x", expected: false},
		{name: "unparsable time", input: "pgtest_abc_1_1", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, isStale(tt.input, now))
		})
	}
}
