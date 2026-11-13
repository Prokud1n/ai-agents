package health

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	postgresName = "postgres"
	redisName    = "redis"
	s3Name       = "s3"
)

func TestBuildReport(t *testing.T) {
	t.Parallel()

	errTimeout := errors.New("context deadline exceeded")

	tests := []struct {
		name         string
		input        []checkResult
		expected     Report
		expectedCode int
	}{
		{
			name:         "no dependencies",
			input:        nil,
			expected:     Report{Status: OKStatus, Dependencies: map[string]Status{}},
			expectedCode: http.StatusOK,
		},
		{
			name:  "all dependencies ok",
			input: []checkResult{{name: postgresName}, {name: redisName}, {name: s3Name}},
			expected: Report{
				Status:       OKStatus,
				Dependencies: map[string]Status{postgresName: OKStatus, redisName: OKStatus, s3Name: OKStatus},
			},
			expectedCode: http.StatusOK,
		},
		{
			name:  "one dependency fails",
			input: []checkResult{{name: postgresName}, {name: redisName, err: errTimeout}, {name: s3Name}},
			expected: Report{
				Status:       FailStatus,
				Dependencies: map[string]Status{postgresName: OKStatus, redisName: FailStatus, s3Name: OKStatus},
			},
			expectedCode: http.StatusServiceUnavailable,
		},
		{
			name:  "all dependencies fail",
			input: []checkResult{{name: postgresName, err: errTimeout}, {name: s3Name, err: errTimeout}},
			expected: Report{
				Status:       FailStatus,
				Dependencies: map[string]Status{postgresName: FailStatus, s3Name: FailStatus},
			},
			expectedCode: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildReport(tt.input)

			assert.Equal(t, tt.expected, got)
			assert.Equal(t, tt.expectedCode, got.HTTPStatus())
		})
	}
}
