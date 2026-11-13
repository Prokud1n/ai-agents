// Command healthcheck is a tiny HTTP probe for the Docker HEALTHCHECK:
// the runtime image is distroless and has neither curl nor wget.
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
	defaultURL     = "http://localhost:8080/readyz"
	requestTimeout = 3 * time.Second
)

var errUnhealthy = errors.New("unhealthy")

func main() {
	url := defaultURL
	if len(os.Args) > 1 {
		url = os.Args[1]
	}

	if err := probe(url); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func probe(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to probe %s: status %d: %w", url, resp.StatusCode, errUnhealthy)
	}

	return nil
}
