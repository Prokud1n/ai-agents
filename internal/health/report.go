package health

import "net/http"

type Status string

const (
	OKStatus   Status = "ok"
	FailStatus Status = "fail"
)

// Report is the /readyz response body. It carries statuses only: error details go to the log,
// so the endpoint does not leak hosts or internals.
type Report struct {
	Status       Status            `json:"status"`
	Dependencies map[string]Status `json:"dependencies"`
}

type checkResult struct {
	name string
	err  error
}

// buildReport aggregates dependency checks: the service is ready only when every dependency is.
func buildReport(results []checkResult) Report {
	report := Report{
		Status:       OKStatus,
		Dependencies: make(map[string]Status, len(results)),
	}

	for _, r := range results {
		if r.err == nil {
			report.Dependencies[r.name] = OKStatus

			continue
		}

		report.Status = FailStatus
		report.Dependencies[r.name] = FailStatus
	}

	return report
}

func (r *Report) HTTPStatus() int {
	if r.Status == OKStatus {
		return http.StatusOK
	}

	return http.StatusServiceUnavailable
}
