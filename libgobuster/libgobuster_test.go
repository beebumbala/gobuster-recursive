package libgobuster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type testResult string

func (r testResult) ResultToString() (string, error) { return string(r), nil }

type passPlugin struct {
	mu      sync.Mutex
	pass    int
	words   [][]string
	started chan struct{}
	block   bool
}

func (*passPlugin) Name() string                            { return "test" }
func (*passPlugin) PreRun(context.Context, *Progress) error { return nil }
func (*passPlugin) GetConfigString() (string, error)        { return "", nil }
func (*passPlugin) AdditionalWords(string) []string         { return nil }
func (*passPlugin) AdditionalWordsLen() int                 { return 0 }
func (*passPlugin) NeedsWordlistReplay() bool               { return true }
func (*passPlugin) AdditionalSuccessWords(word string) []string {
	if word == "hit" {
		return []string{"extra"}
	}
	return nil
}
func (p *passPlugin) ProcessWord(ctx context.Context, word string, _ *Progress) (Result, error) {
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	if p.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p.mu.Lock()
	p.words[p.pass] = append(p.words[p.pass], word)
	p.mu.Unlock()
	if word == "hit" {
		return testResult(word), nil
	}
	if word == "error" {
		return nil, errors.New("test error")
	}
	return nil, nil // nolint:nilnil
}
func (p *passPlugin) NextPass(context.Context, *Progress) (bool, error) {
	p.pass++
	return p.pass < len(p.words), nil
}

func drainProgress(g *Gobuster) func() {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for range g.Progress.ResultChan {
		}
	}()
	go func() {
		defer wg.Done()
		for range g.Progress.ErrorChan {
		}
	}()
	go func() {
		defer wg.Done()
		for range g.Progress.MessageChan {
		}
	}()
	return wg.Wait
}

func TestPassBarrierIncludesExtraAndFailedWork(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "words")
	if err := os.WriteFile(file, []byte("hit\nmiss\nerror\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := &passPlugin{words: make([][]string, 2)}
	g, _ := NewGobuster(&Options{Wordlist: file, Threads: 3}, plugin, NewLogger(false))
	wait := drainProgress(g)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := g.Run(ctx); err != nil {
		t.Fatal(err)
	}
	wait()
	for pass, words := range plugin.words {
		slices.Sort(words)
		if !slices.Equal(words, []string{"error", "extra", "hit", "miss"}) {
			t.Errorf("incomplete pass %d: %v", pass, words)
		}
	}
	if g.Progress.RequestsIssued() != 8 || g.Progress.RequestsExpected() != 8 {
		t.Fatal("incorrect accumulated progress")
	}
}

func TestEmptyWordlistCompletes(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "\n \n"} {
		file := filepath.Join(t.TempDir(), "words")
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		plugin := &passPlugin{words: make([][]string, 2)}
		g, _ := NewGobuster(&Options{Wordlist: file, Threads: 2}, plugin, NewLogger(false))
		wait := drainProgress(g)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		err := g.Run(ctx)
		cancel()
		wait()
		if err != nil {
			t.Fatal(err)
		}
		if g.Progress.RequestsExpected() != 0 {
			t.Fatal("empty wordlist counted as work")
		}
	}
}

func TestCancelActivePass(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "words")
	if err := os.WriteFile(file, []byte("hit\nmiss\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plugin := &passPlugin{words: make([][]string, 2), started: make(chan struct{}, 1), block: true}
	g, _ := NewGobuster(&Options{Wordlist: file, Threads: 2}, plugin, NewLogger(false))
	wait := drainProgress(g)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	select {
	case <-plugin.started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation blocked")
	}
	wait()
	if plugin.pass != 0 {
		t.Fatal("started another pass after cancellation")
	}
}

func TestStdinReplayAndCancellation(t *testing.T) {
	// These subtests replace process stdin, so they intentionally run serially.
	for _, cancelInput := range []bool{false, true} {
		t.Run(map[bool]string{false: "replay", true: "cancel-blocked-input"}[cancelInput], func(t *testing.T) {
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer writer.Close()
			original := os.Stdin
			os.Stdin = input
			defer func() { os.Stdin = original }()
			t.Setenv("TMP", t.TempDir())
			plugin := &passPlugin{words: make([][]string, 2), started: make(chan struct{}, 1)}
			g, _ := NewGobuster(&Options{Wordlist: "-", Threads: 2}, plugin, NewLogger(false))
			wait := drainProgress(g)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- g.Run(ctx) }()
			if _, err := writer.WriteString("hit\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-plugin.started:
			case <-time.After(3 * time.Second):
				t.Fatal("stdin was buffered until EOF")
			}
			if cancelInput {
				cancel()
			} else {
				writer.Close()
			}
			select {
			case err := <-done:
				if cancelInput && !errors.Is(err, context.Canceled) || !cancelInput && err != nil {
					t.Fatalf("run: %v", err)
				}
			case <-time.After(3 * time.Second):
				writer.Close()
				t.Fatal("stdin scan did not finish")
			}
			wait()
			if !cancelInput {
				for _, words := range plugin.words {
					slices.Sort(words)
					if !slices.Equal(words, []string{"extra", "hit"}) {
						t.Fatalf("bad stdin replay: %v", plugin.words)
					}
				}
			}
			entries, err := os.ReadDir(os.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "gobuster-wordlist-") {
					t.Error("stdin cache was not removed")
				}
			}
		})
	}
}
