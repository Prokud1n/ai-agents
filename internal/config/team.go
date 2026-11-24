package config

import (
	"fmt"
	"time"
	// The time zone database is built in: the images have no /usr/share/zoneinfo.
	_ "time/tzdata"
)

// Team configures what the team chat shows the trainer.
type Team struct {
	// Timezone is the IANA zone of the dates the trainer reads (/ideas).
	Timezone string `env:"TEAM_TIMEZONE" envDefault:"Europe/Moscow"`
}

// Location loads TEAM_TIMEZONE; an unknown zone is an error, so a typo stops the start.
func (t *Team) Location() (*time.Location, error) {
	location, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return nil, fmt.Errorf("failed to load TEAM_TIMEZONE %q: %w", t.Timezone, err)
	}

	return location, nil
}
