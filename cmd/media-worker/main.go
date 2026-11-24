// Command media-worker edits the editor's videos: it takes jobs from Redis, edits the sources from S3 with ffmpeg
// (transcription, rough cut, brand subtitles, Instagram export) and stores the reels back in S3. It runs in the media
// image, apart from the app. "media-worker healthcheck" is the container healthcheck.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Prokud1n/ai-agents/internal"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := internal.CheckMediaWorkerHealth(time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

		return
	}

	internal.NewMediaWorker().Run()
}
