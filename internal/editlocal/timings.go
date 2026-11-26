package editlocal

import (
	"fmt"
	"strings"
	"time"
)

// stage is the part of the edit the time between two runs of the executor goes to.
type stage string

const (
	// planStage: the auto job of the first version waits in the queue, is transcribed and planned (or rendered at
	// once when there is nothing to decide).
	planStage stage = "plan"
	// renderStage: the render job of a decided version waits in the queue and is rendered.
	renderStage stage = "render"
	// decideStage: the model decides on a revision; no job is submitted yet. Its time outside the model counts to the
	// render, as before the stage was told apart.
	decideStage stage = "decide"
)

// Timings of a local edit by stage.
type Timings struct {
	Upload   time.Duration
	Plan     time.Duration
	Model    time.Duration
	Render   time.Duration
	Download time.Duration
	Total    time.Duration
}

// add counts elapsed time of the stage, of which model went to the model's decisions.
func (t *Timings) add(s stage, elapsed, model time.Duration) {
	model = min(max(model, 0), elapsed)
	t.Model += model

	switch s {
	case renderStage, decideStage:
		t.Render += elapsed - model
	default:
		t.Plan += elapsed - model
	}
}

type timing struct {
	label    string
	duration time.Duration
}

func (t *Timings) items() []timing {
	return []timing{
		{label: "загрузка исходника в S3", duration: t.Upload},
		{label: "воркер: очередь, транскрипция и план (или сразу рендер)", duration: t.Plan},
		{label: "модель Монтажёра", duration: t.Model},
		{label: "воркер: рендер решённой версии", duration: t.Render},
		{label: "скачивание результатов", duration: t.Download},
		{label: "итого", duration: t.Total},
	}
}

// Format is the content of timings.txt: a stage a line.
func (t *Timings) Format() string {
	var b strings.Builder

	for _, item := range t.items() {
		fmt.Fprintf(&b, "%s: %s\n", item.label, seconds(item.duration))
	}

	return b.String()
}

// Summary is the timings in one line.
func (t *Timings) Summary() string {
	items := t.items()
	parts := make([]string, 0, len(items))

	for _, item := range items {
		parts = append(parts, item.label+" "+seconds(item.duration))
	}

	return strings.Join(parts, "; ")
}

func seconds(d time.Duration) string {
	return fmt.Sprintf("%.1f с", d.Seconds())
}
