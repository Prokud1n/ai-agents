package health

import "net/http"

type Status string

const (
	OKStatus   Status = "ok"
	FailStatus Status = "fail"
)

// DependencyReport is the readiness of a single dependency.
type DependencyReport struct {
	Status Status `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Report is the /readyz response body.
type Report struct {
	Status       Status                      `json:"status"`
	Dependencies map[string]DependencyReport `json:"dependencies"`
}

type checkResult struct {
	name string
	err  error
}

// buildReport aggregates dependency checks: the service is ready only when every dependency is.
func buildReport(results []checkResult) Report {
	report := Report{
		Status:       OKStatus,
		Dependencies: make(map[string]DependencyReport, len(results)),
	}

	for _, r := range results {
		if r.err == nil {
			report.Dependencies[r.name] = DependencyReport{Status: OKStatus}

			continue
		}

		report.Status = FailStatus
		report.Dependencies[r.name] = DependencyReport{Status: FailStatus, Error: r.err.Error()}
	}

	return report
}

func (r *Report) HTTPStatus() int {
	if r.Status == OKStatus {
		return http.StatusOK
	}

	return http.StatusServiceUnavailable
}
