package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

// terminalOutput serializes stdout, stderr and progress updates
type terminalOutput struct {
	mu           sync.Mutex
	stdout       io.Writer
	stderr       io.Writer
	width        func() int
	lastProgress string
}

type terminalWriter struct {
	output *terminalOutput
	writer io.Writer
}

func (w terminalWriter) Write(p []byte) (int, error) {
	w.output.mu.Lock()
	defer w.output.mu.Unlock()
	w.output.clearProgress()
	return w.writer.Write(p)
}

func (o *terminalOutput) stdoutWriter() io.Writer {
	return terminalWriter{output: o, writer: o.stdout}
}

func (o *terminalOutput) stderrWriter() io.Writer {
	return terminalWriter{output: o, writer: o.stderr}
}

// clearProgress is called only while holding mu.
func (o *terminalOutput) clearProgress() {
	if o.lastProgress != "" {
		_, _ = fmt.Fprint(o.stderr, "\r", strings.Repeat(" ", len(o.lastProgress)), "\r")
		o.lastProgress = ""
	}
}

func (o *terminalOutput) progress(text string, final bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.clearProgress()
	width := o.width()
	if width < 2 {
		return
	}
	// Leave the last column unused to prevent automatic line wrapping
	if len(text) >= width {
		if width > 4 {
			text = text[:width-4] + "..."
		} else {
			text = text[:width-1]
		}
	}
	if final {
		_, _ = fmt.Fprintln(o.stderr, text)
		return
	}
	_, _ = fmt.Fprint(o.stderr, text)
	o.lastProgress = text
}
