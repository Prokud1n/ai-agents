package config

// Pilot configures the weekly pilot report to «Техничка» (internal/teamchat): the metrics of the week and the trainer's
// feedback. The day and the time are in TEAM_TIMEZONE; both empty turn the report off.
type Pilot struct {
	// ReportWeekday is an English weekday name, e.g. monday (case-insensitive); empty: no report.
	ReportWeekday string `env:"PILOT_REPORT_WEEKDAY"`
	// ReportTime is HH:MM in TEAM_TIMEZONE, e.g. 10:00; empty: no report.
	ReportTime string `env:"PILOT_REPORT_TIME"`
}

// ReportEnabled reports whether any of the schedule is set: then both must be valid.
func (p *Pilot) ReportEnabled() bool {
	return p.ReportWeekday != "" || p.ReportTime != ""
}
