package ffmpeg

import (
	"bytes"
	"strings"
)

// tailBuffer is a ring buffer that keeps the last len(buf) bytes written to it: ffmpeg can log a lot,
// but only the end is needed to explain a failure.
type tailBuffer struct {
	buf  []byte
	next int
	full bool
}

func newTailBuffer(size int) *tailBuffer {
	return &tailBuffer{buf: make([]byte, size)}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	size := len(b.buf)
	if len(p) >= size {
		copy(b.buf, p[len(p)-size:])
		b.next, b.full = 0, true

		return len(p), nil
	}

	copied := copy(b.buf[b.next:], p)
	if copied < len(p) {
		copy(b.buf, p[copied:])
	}

	if b.next+len(p) >= size {
		b.full = true
	}

	b.next = (b.next + len(p)) % size

	return len(p), nil
}

func (b *tailBuffer) String() string {
	if !b.full {
		return string(b.buf[:b.next])
	}

	return string(b.buf[b.next:]) + string(b.buf[:b.next])
}

// lineCollector keeps only the log lines that contain a marker (silencedetect results), up to a limit,
// so parsing a long file's log needs no unbounded buffer.
type lineCollector struct {
	marker   string
	maxLines int
	maxLine  int
	partial  []byte
	overflow bool // the current line is longer than maxLine: skipped up to its end
	lines    []string
}

func (c *lineCollector) Write(p []byte) (int, error) {
	data := p

	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			c.appendPartial(data)

			break
		}

		c.appendPartial(data[:i])

		if !c.overflow {
			c.keep(string(c.partial))
		}

		c.partial, c.overflow = c.partial[:0], false
		data = data[i+1:]
	}

	return len(p), nil
}

func (c *lineCollector) appendPartial(p []byte) {
	if c.overflow || len(c.partial)+len(p) > c.maxLine {
		c.partial, c.overflow = c.partial[:0], true

		return
	}

	c.partial = append(c.partial, p...)
}

func (c *lineCollector) keep(line string) {
	if len(c.lines) < c.maxLines && strings.Contains(line, c.marker) {
		c.lines = append(c.lines, line)
	}
}

// String returns the collected lines and an unterminated last line, if it has the marker.
func (c *lineCollector) String() string {
	lines := c.lines
	if tail := string(c.partial); !c.overflow && strings.Contains(tail, c.marker) && len(lines) < c.maxLines {
		lines = append(lines[:len(lines):len(lines)], tail)
	}

	return strings.Join(lines, "\n")
}
