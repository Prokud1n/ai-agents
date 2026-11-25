package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLongerThanTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration float64
		target   float64
		expected bool
	}{
		{name: "within the target", duration: 55, target: 60},
		{name: "within the 10 % margin", duration: 66, target: 60},
		{name: "longer than the margin", duration: 66.1, target: 60, expected: true},
		{name: "no target", duration: 600},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, LongerThanTarget(tt.duration, tt.target))
		})
	}
}
