// Command healthcheck is a tiny HTTP probe for the container healthcheck:
// the runtime image is distroless and has neither curl nor wget.
// The address is fixed: inside the container the app always listens on :8080.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

const (
	readinessURL   = "http://localhost:8080/readyz"
	requestTimeout = 3 * time.Second
)

var errUnhealthy = errors.New("unhealthy")

func main() {
	if err := probe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func probe() error {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readinessURL, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to probe readiness: status %d: %w", resp.StatusCode, errUnhealthy)
	}

	return nil
}
