package editjob

import (
	"fmt"
	"strconv"

	"github.com/Prokud1n/ai-agents/internal/media"
)

// Extensions and content types of the result files.
const (
	VideoExtension = ".mp4"
	JSONExtension  = ".json"
	LogExtension   = ".log"

	VideoContentType = "video/mp4"
	JSONContentType  = "application/json"
	LogContentType   = "text/plain; charset=utf-8"
)

type resultKeys interface {
	ResultKey(sourceKey, name, ext string) (string, error)
}

// ResultFiles are the S3 keys of a job's files, next to the source by its message (media.ResultKey):
//
//	<result prefix>/<chat id>/<message id>/<file unique id>-v<version>-reel.mp4  the reel
//	…/<file unique id>-v<version>-plan.json      the plan of the cut (the next step edits it)
//	…/<file unique id>-v<version>-decisions.log  why each piece was cut
//	…/<file unique id>-transcript.json            the transcript of the source: an edit needs no new transcription
type ResultFiles struct {
	Reel       string
	Plan       string
	Decisions  string
	Transcript string
}

// Files computes the keys of the job's files; the app and the worker get the same ones.
func Files(keys resultKeys, sourceKey string, version int) (ResultFiles, error) {
	source, err := media.ParseKey(sourceKey)
	if err != nil {
		return ResultFiles{}, fmt.Errorf("failed to read source key: %w", err)
	}

	versioned := source.Name + "-v" + strconv.Itoa(version) + "-"

	var files ResultFiles

	for _, f := range []struct {
		key  *string
		name string
		ext  string
	}{
		{key: &files.Reel, name: versioned + "reel", ext: VideoExtension},
		{key: &files.Plan, name: versioned + "plan", ext: JSONExtension},
		{key: &files.Decisions, name: versioned + "decisions", ext: LogExtension},
		{key: &files.Transcript, name: source.Name + "-transcript", ext: JSONExtension},
	} {
		if *f.key, err = keys.ResultKey(sourceKey, f.name, f.ext); err != nil {
			return ResultFiles{}, fmt.Errorf("failed to make result key: %w", err)
		}
	}

	return files, nil
}
