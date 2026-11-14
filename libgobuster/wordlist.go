package libgobuster

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// wordlistSource reuses an open file. Stdin is spooled only for plugins that
// request replay; its first pass still starts before EOF.
type wordlistSource struct {
	file      *os.File
	stdin     *os.File
	temporary string
	first     bool
	offset    int
	words     int
	skipped   int
}

func openWordlist(name string, offset int, replay bool) (*wordlistSource, error) {
	w := &wordlistSource{first: true, offset: offset}
	if offset < 0 {
		return nil, errors.New("wordlist offset must not be negative")
	}
	if name == "-" {
		if offset != 0 {
			return nil, errors.New("wordlist offset is not supported with stdin")
		}
		w.stdin = os.Stdin
		if replay {
			f, err := os.CreateTemp("", "gobuster-wordlist-*")
			if err != nil {
				return nil, fmt.Errorf("cache stdin: %w", err)
			}
			w.file, w.temporary = f, f.Name()
		}
		return w, nil
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open wordlist: %w", err)
	}
	w.file = f
	if err := w.count(); err != nil {
		w.close()
		return nil, err
	}
	return w, nil
}

func (w *wordlistSource) count() error {
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	w.words, w.skipped = 0, 0
	scanner := bufio.NewScanner(w.file)
	lines := 0
	for scanner.Scan() {
		lines++
		if strings.TrimSpace(scanner.Text()) != "" {
			w.words++
			if lines <= w.offset {
				w.skipped++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read wordlist: %w", err)
	}
	if w.offset > 0 && w.offset >= lines {
		return errors.New("offset is greater than or equal to the number of lines in the wordlist")
	}
	return nil
}

func (w *wordlistSource) next() (*bufio.Scanner, int, error) {
	first := w.first
	w.first = false
	if w.stdin != nil && first {
		var input io.Reader = w.stdin
		if w.file != nil {
			input = io.TeeReader(input, w.file)
		}
		return bufio.NewScanner(input), -1, nil
	}
	if w.file == nil {
		return nil, 0, errors.New("plugin requested another pass without a replayable wordlist")
	}
	if w.stdin != nil {
		if err := w.count(); err != nil {
			return nil, 0, err
		}
		w.stdin = nil
	}
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("rewind wordlist: %w", err)
	}
	scanner := bufio.NewScanner(w.file)
	expected := w.words
	if first {
		for range w.offset {
			if !scanner.Scan() {
				return nil, 0, fmt.Errorf("skip wordlist offset: %w", io.ErrUnexpectedEOF)
			}
		}
		expected -= w.skipped
	}
	return scanner, expected, nil
}

func (w *wordlistSource) close() {
	if w.file != nil {
		_ = w.file.Close()
	}
	if w.temporary != "" {
		_ = os.Remove(w.temporary)
	}
}
