package health

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildReport(t *testing.T) {
	t.Parallel()

	errTimeout := errors.New("context deadline exceeded")
	errBucket := errors.New("bucket not found")

	tests := []struct {
		name         string
		input        []checkResult
		expected     Report
		expectedCode int
	}{
		{
			name:  "no dependencies",
			input: nil,
			expected: Report{
				Status:       OKStatus,
				Dependencies: map[string]DependencyReport{},
			},
			expectedCode: http.StatusOK,
		},
		{
			name: "all dependencies ok",
			input: []checkResult{
				{name: postgresDependency},
				{name: redisDependency},
				{name: s3Dependency},
			},
			expected: Report{
				Status: OKStatus,
				Dependencies: map[string]DependencyReport{
					postgresDependency: {Status: OKStatus},
					redisDependency:    {Status: OKStatus},
					s3Dependency:       {Status: OKStatus},
				},
			},
			expectedCode: http.StatusOK,
		},
		{
			name: "one dependency fails",
			input: []checkResult{
				{name: postgresDependency},
				{name: redisDependency, err: errTimeout},
				{name: s3Dependency},
			},
			expected: Report{
				Status: FailStatus,
				Dependencies: map[string]DependencyReport{
					postgresDependency: {Status: OKStatus},
					redisDependency:    {Status: FailStatus, Error: errTimeout.Error()},
					s3Dependency:       {Status: OKStatus},
				},
			},
			expectedCode: http.StatusServiceUnavailable,
		},
		{
			name: "all dependencies fail",
			input: []checkResult{
				{name: postgresDependency, err: errTimeout},
				{name: s3Dependency, err: errBucket},
			},
			expected: Report{
				Status: FailStatus,
				Dependencies: map[string]DependencyReport{
					postgresDependency: {Status: FailStatus, Error: errTimeout.Error()},
					s3Dependency:       {Status: FailStatus, Error: errBucket.Error()},
				},
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
