package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/OJ/gobuster/v3/libgobuster"
)

// screen models carriage returns without erasing the remainder of the line,
// reproducing the Windows console behavior that exposed the progress bug.
type screen struct {
	line   []rune
	column int
	lines  []string
}

func (s *screen) Write(p []byte) (int, error) {
	for _, c := range string(p) {
		switch c {
		case '\r':
			s.column = 0
		case '\n':
			s.lines = append(s.lines, strings.TrimRight(string(s.line), " "))
			s.line, s.column = nil, 0
		default:
			for len(s.line) <= s.column {
				s.line = append(s.line, ' ')
			}
			s.line[s.column] = c
			s.column++
		}
	}
	return len(p), nil
}

func testOutput(width int) (*terminalOutput, *screen) {
	s := &screen{}
	return &terminalOutput{stdout: s, stderr: s, width: func() int { return width }}, s
}

func TestProgressClearedBeforeResultsAndLogs(t *testing.T) {
	t.Parallel()
	output, terminal := testOutput(120)
	logger := libgobuster.NewLogger(true)
	logger.SetOutput(output.stdoutWriter(), output.stderrWriter())
	progress := "Progress: 4542 / 4613 (98.46%) | Depth: 0 | http://localhost:8000/"
	output.progress(progress, false)
	fmt.Fprintln(output.stdoutWriter(), "robots.txt (Status: 200)")
	output.progress(progress, false)
	logger.Info("Scanning directory http://localhost:8000/admin/ (depth 1)")
	output.progress(progress, false)
	logger.Warn("Skipping directory")
	output.progress(progress, false)
	logger.Error("Request failed")
	output.progress(progress, false)
	logger.Debugf("Request %s", "/admin/")
	output.progress("Progress: 100 / 100 (100.00%)", true)
	want := []string{
		"robots.txt (Status: 200)",
		"[INFO] Scanning directory http://localhost:8000/admin/ (depth 1)",
		"[WARN] Skipping directory",
		"[ERROR] Request failed",
		"[DEBUG] Request /admin/",
		"Progress: 100 / 100 (100.00%)",
	}
	if strings.Join(terminal.lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("overlapping console output:\n%s", strings.Join(terminal.lines, "\n"))
	}
}

func TestProgressShorterTargetAndNarrowTerminal(t *testing.T) {
	t.Parallel()
	for _, width := range []int{3, 40, 80, 120} {
		output, terminal := testOutput(width)
		output.progress("Progress: 9225 / 9226 (99.99%) | Depth: 1 | http://localhost:8000/"+strings.Repeat("long-directory/", 20), false)
		if terminal.column >= width {
			t.Fatalf("progress wraps at width %d: %d columns", width, terminal.column)
		}
		output.progress("OK", false)
		if strings.TrimRight(string(terminal.line), " ") != "OK" {
			t.Fatalf("stale progress after shortening: %q", string(terminal.line))
		}
		fmt.Fprintln(output.stdoutWriter(), "found")
		if terminal.lines[0] != "found" {
			t.Fatalf("progress left a suffix: %q", terminal.lines[0])
		}
	}
}

func TestConcurrentResultsAndProgress(t *testing.T) {
	t.Parallel()
	output, terminal := testOutput(80)
	var workers sync.WaitGroup
	for worker := range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range 50 {
				output.progress("Progress: 50 / 100 (50.00%) | http://localhost:8000/admin/", false)
				fmt.Fprintf(output.stdoutWriter(), "result %d/%d\n", worker, i)
			}
		}()
	}
	workers.Wait()
	output.progress("Progress: 100 / 100 (100.00%)", true)
	if len(terminal.lines) != 201 {
		t.Fatalf("lost or wrapped lines: got %d", len(terminal.lines))
	}
	for _, line := range terminal.lines[:200] {
		if !strings.HasPrefix(line, "result ") || strings.Contains(line, "http") || strings.Contains(line, "Progress") {
			t.Fatalf("mixed progress and result: %q", line)
		}
	}
}

type outputResult string

func (r outputResult) ResultToString() (string, error) { return string(r), nil }

func TestResultsFileHasNoProgressControlCharacters(t *testing.T) {
	t.Parallel()
	output, terminal := testOutput(80)
	logger := libgobuster.NewLogger(false)
	logger.SetOutput(output.stdoutWriter(), output.stderrWriter())
	g, err := libgobuster.NewGobuster(&libgobuster.Options{}, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "results.txt")
	output.progress("Progress: 1 / 2 (50.00%) | http://localhost:8000/admin/", false)
	var workers sync.WaitGroup
	workers.Add(1)
	go resultWorker(g, file, output.stdoutWriter(), &workers)
	g.Progress.ResultChan <- outputResult("admin/test (Status: 200)\n")
	close(g.Progress.ResultChan)
	workers.Wait()
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "admin/test (Status: 200)\n" || terminal.lines[0] != "admin/test (Status: 200)" {
		t.Fatalf("unexpected result output: file=%q terminal=%v", content, terminal.lines)
	}
}

func TestOutputWithoutProgressIsPlainText(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	output := &terminalOutput{stdout: &stdout, stderr: &stderr}
	fmt.Fprintln(output.stdoutWriter(), "result")
	fmt.Fprintln(output.stderrWriter(), "message")
	if stdout.String() != "result\n" || stderr.String() != "message\n" {
		t.Fatal("added terminal controls when progress is disabled")
	}
}
