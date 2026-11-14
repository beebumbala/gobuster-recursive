package libgobuster

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// PATTERN is the pattern for wordlist replacements in pattern file
const PATTERN = "{GOBUSTER}"

// SetupFunc is the "setup" function prototype for implementations
type SetupFunc func(*Gobuster) error

// ProcessFunc is the "process" function prototype for implementations
type ProcessFunc func(*Gobuster, string) ([]Result, error)

// ResultToStringFunc is the "to string" function prototype for implementations
type ResultToStringFunc func(*Gobuster, *Result) (*string, error)

// Gobuster is the main object when creating a new run
type Gobuster struct {
	Opts     *Options
	Logger   *Logger
	plugin   GobusterPlugin
	Progress *Progress
}

type Guess struct {
	word              string
	discoverOnSuccess bool
}

// NewGobuster returns a new Gobuster object
func NewGobuster(opts *Options, plugin GobusterPlugin, logger *Logger) (*Gobuster, error) {
	var g Gobuster
	g.Opts = opts
	g.plugin = plugin
	g.Logger = logger
	g.Progress = NewProgress()

	return &g, nil
}

type completedGuess struct {
	guess *Guess
	found bool
}

func (g *Gobuster) worker(ctx context.Context, guesses <-chan *Guess, completed chan<- completedGuess, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case guess := <-guesses:
			if ctx.Err() != nil {
				return
			}
			res, err := g.plugin.ProcessWord(ctx, guess.word, g.Progress)
			if err != nil {
				g.Progress.ErrorChan <- fmt.Errorf("error on word %s: %w", guess.word, err)
			}
			if res != nil {
				g.Progress.ResultChan <- res
			}
			g.Progress.incrementRequests()
			if g.Opts.Delay > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(g.Opts.Delay):
				}
			}
			select {
			case <-ctx.Done():
				return
			case completed <- completedGuess{guess: guess, found: res != nil}:
			}
		}
	}
}

// runPass owns admission and completion accounting. Progress counters are only
// for display: EOF plus no queued or active work is the completion barrier.
func (g *Gobuster) runPass(ctx context.Context, scanner *bufio.Scanner, expected int, guesses chan<- *Guess, completed <-chan completedGuess) error {
	passCtx, cancel := context.WithCancel(ctx)
	words := make(chan string)
	readDone := make(chan error, 1)
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		defer func() {
			readDone <- scanner.Err()
			close(words)
		}()
		for scanner.Scan() {
			word := strings.TrimSpace(scanner.Text())
			if word == "" {
				continue
			}
			select {
			case <-passCtx.Done():
				return
			case words <- word:
			}
		}
	}()
	defer func() {
		cancel()
		readers.Wait()
	}()

	factor := 1 + g.plugin.AdditionalWordsLen()
	if len(g.Opts.Patterns) > 0 {
		factor *= len(g.Opts.Patterns)
	}
	if expected >= 0 {
		g.Progress.IncrementTotalRequests(expected * factor)
	}
	var pending []*Guess
	active := 0
	for words != nil || len(pending) > 0 || active > 0 {
		var input <-chan string
		var output chan<- *Guess
		var next *Guess
		if len(pending) == 0 {
			input = words
		} else {
			output, next = guesses, pending[0]
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case word, ok := <-input:
			if !ok {
				words = nil
				if err := <-readDone; err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return fmt.Errorf("read wordlist: %w", err)
				}
				continue
			}
			variants := []string{word}
			if len(g.Opts.Patterns) > 0 {
				variants = g.processPatterns(word)
			}
			for _, variant := range variants {
				pending = append(pending, &Guess{word: variant, discoverOnSuccess: true})
				for _, additional := range g.plugin.AdditionalWords(variant) {
					pending = append(pending, &Guess{word: additional, discoverOnSuccess: true})
				}
			}
			if expected < 0 {
				g.Progress.IncrementTotalRequests(len(pending))
			}
		case output <- next:
			pending[0] = nil
			pending = pending[1:]
			active++
		case done := <-completed:
			if done.found && done.guess.discoverOnSuccess {
				extra := g.plugin.AdditionalSuccessWords(done.guess.word)
				extra = append(extra, g.processDiscoverPatterns(done.guess.word)...)
				g.Progress.IncrementTotalRequests(len(extra))
				for _, word := range extra {
					pending = append(pending, &Guess{word: word})
				}
			}
			active--
		}
	}
	return ctx.Err()
}

// Run keeps one worker pool and one set of result channels for every pass.
func (g *Gobuster) Run(ctx context.Context) error {
	defer close(g.Progress.ResultChan)
	defer close(g.Progress.ErrorChan)
	defer close(g.Progress.MessageChan)
	if g.Opts.Threads < 1 {
		return errors.New("threads must be greater than zero")
	}
	if err := g.plugin.PreRun(ctx, g.Progress); err != nil {
		return err
	}
	replay := false
	if plugin, ok := g.plugin.(ReplayPlugin); ok {
		replay = plugin.NeedsWordlistReplay()
	}
	wordlist, err := openWordlist(g.Opts.Wordlist, g.Opts.WordlistOffset, replay)
	if err != nil {
		return err
	}
	defer wordlist.close()
	// Closing a pipe on cancellation unblocks Scanner.Read. Capture the input
	// now: the source switches to its replay file after the first pass.
	if input := wordlist.stdin; input != nil {
		stop := context.AfterFunc(ctx, func() { _ = input.Close() })
		defer stop()
	}

	workerCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	guesses := make(chan *Guess)
	completed := make(chan completedGuess)
	for range g.Opts.Threads {
		workers.Add(1)
		go g.worker(workerCtx, guesses, completed, &workers)
	}
	defer func() {
		cancel()
		workers.Wait()
	}()
	for {
		scanner, expected, err := wordlist.next()
		if err != nil {
			return err
		}
		if err := g.runPass(ctx, scanner, expected, guesses, completed); err != nil {
			return err
		}
		plugin, ok := g.plugin.(MultiPassPlugin)
		if !ok {
			return nil
		}
		more, err := plugin.NextPass(ctx, g.Progress)
		if err != nil || !more {
			return err
		}
	}
}

// GetConfigString returns the current config as a printable string
func (g *Gobuster) GetConfigString() (string, error) {
	return g.plugin.GetConfigString()
}

func (g *Gobuster) processPatterns(word string) []string {
	if g.Opts.PatternFile == "" {
		return nil
	}

	return g.applyPatterns(word, g.Opts.Patterns)
}

func (g *Gobuster) processDiscoverPatterns(word string) []string {
	if g.Opts.DiscoverPatternFile == "" {
		return nil
	}

	return g.applyPatterns(word, g.Opts.DiscoverPatterns)
}

func (g *Gobuster) applyPatterns(word string, patterns []string) []string {
	pat := make([]string, len(patterns))
	for i, x := range patterns {
		pat[i] = strings.ReplaceAll(x, PATTERN, word)
	}
	return pat
}
