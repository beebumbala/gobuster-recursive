package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/OJ/gobuster/v3/libgobuster"
	"golang.org/x/term"
)

const (
	ruler             = "==============================================================="
	cliProgressUpdate = 500 * time.Millisecond
)

// resultWorker outputs the results as they come in. This needs to be a range and should not handle
// the context so the channel always has a receiver and libgobuster will not block.
func resultWorker(g *libgobuster.Gobuster, filename string, output io.Writer, wg *sync.WaitGroup) {
	defer wg.Done()

	var f *os.File
	var err error
	if filename != "" {
		f, err = os.Create(filename)
		if err != nil {
			g.Logger.Fatalf("error on creating output file: %v", err)
		}
		defer f.Close()
	}

	for r := range g.Progress.ResultChan {
		s, err := r.ResultToString()
		if err != nil {
			g.Logger.Fatal(err)
		}
		if s != "" {
			s = strings.TrimSpace(s)
			_, _ = fmt.Fprintln(output, s)
			if f != nil {
				err = writeToFile(f, s)
				if err != nil {
					g.Logger.Fatalf("error on writing output file: %v", err)
				}
			}
		}
	}
}

// errorWorker outputs the errors as they come in. This needs to be a range and should not handle
// the context so the channel always has a receiver and libgobuster will not block.
func errorWorker(g *libgobuster.Gobuster, wg *sync.WaitGroup) {
	defer wg.Done()

	for e := range g.Progress.ErrorChan {
		if !g.Opts.Quiet && !g.Opts.NoError {
			g.Logger.Error(e.Error())
			g.Logger.Debugf("%#v", e)
		}
	}
}

// messageWorker outputs messages as they come in. This needs to be a range and should not handle
// the context so the channel always has a receiver and libgobuster will not block.
func messageWorker(g *libgobuster.Gobuster, wg *sync.WaitGroup) {
	defer wg.Done()

	for msg := range g.Progress.MessageChan {
		if !g.Opts.Quiet {
			switch msg.Level {
			case libgobuster.LevelDebug:
				g.Logger.Debug(msg.Message)
			case libgobuster.LevelError:
				g.Logger.Error(msg.Message)
			case libgobuster.LevelWarn:
				g.Logger.Warn(msg.Message)
			case libgobuster.LevelInfo:
				g.Logger.Info(msg.Message)
			default:
				panic(fmt.Sprintf("invalid level %d", msg.Level))
			}
		}
	}
}

func printProgress(g *libgobuster.Gobuster, output *terminalOutput, final bool) {
	requestsIssued := g.Progress.RequestsIssued()
	requestsExpected := g.Progress.RequestsExpected()
	if requestsExpected == 0 {
		requestsExpected = 1 // avoid division by zero
	}
	percent := float32(requestsIssued) * 100.0 / float32(requestsExpected)
	if math.IsNaN(float64(percent)) {
		percent = 0.0
	}
	s := fmt.Sprintf("Progress: %d / %d (%3.2f%%)", requestsIssued, requestsExpected, percent)
	if target, depth := g.Progress.Target(); target != "" {
		s += fmt.Sprintf(" | Depth: %d | %s", depth, target)
	}
	output.progress(s, final)
}

// progressWorker outputs the progress every tick. It will stop once cancel() is called
// on the context
func progressWorker(ctx context.Context, g *libgobuster.Gobuster, output *terminalOutput, wg *sync.WaitGroup) {
	defer wg.Done()

	tick := time.NewTicker(cliProgressUpdate)
	defer tick.Stop()

	for {
		select {
		case <-tick.C:
			printProgress(g, output, false)
		case <-ctx.Done():
			// print the final progress so we end at 100%
			printProgress(g, output, true)
			return
		}
	}
}

func writeToFile(f *os.File, output string) error {
	_, err := fmt.Fprintf(f, "%s\n", output)
	if err != nil {
		return fmt.Errorf("[!] Unable to write to file %w", err)
	}
	return nil
}

// Gobuster is the main entry point for the CLI
func Gobuster(ctx context.Context, opts *libgobuster.Options, plugin libgobuster.GobusterPlugin, log *libgobuster.Logger) error {
	// Sanity checks
	if opts == nil {
		return errors.New("please provide valid options")
	}

	if plugin == nil {
		return errors.New("please provide a valid plugin")
	}

	ctxCancel, cancel := context.WithCancel(ctx)
	defer cancel()

	gobuster, err := libgobuster.NewGobuster(opts, plugin, log)
	if err != nil {
		return err
	}

	if !opts.Quiet {
		log.Println(ruler)
		log.Printf("Gobuster v%s\n", libgobuster.VERSION)
		log.Println("by OJ Reeves (@TheColonial) & Christian Mehlmauer (@firefart)")
		log.Println(ruler)
		c, err := gobuster.GetConfigString()
		if err != nil {
			return fmt.Errorf("error on creating config string: %w", err)
		}
		log.Println(c)
		log.Println(ruler)
		gobuster.Logger.Printf("Starting gobuster in %s mode", plugin.Name())
		if opts.WordlistOffset > 0 {
			gobuster.Logger.Printf("Skipping the first %d elements...", opts.WordlistOffset)
		}
		log.Println(ruler)
	}

	// Both streams share the progress line only when attached to a terminal.
	if !term.IsTerminal(int(os.Stdout.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		opts.NoProgress = true
	}
	output := &terminalOutput{
		stdout: os.Stdout,
		stderr: os.Stderr,
		width: func() int {
			width, _, err := term.GetSize(int(os.Stderr.Fd()))
			if err != nil {
				return 80
			}
			return width
		},
	}
	log.SetOutput(output.stdoutWriter(), output.stderrWriter())
	defer log.SetOutput(os.Stdout, os.Stderr)

	// our waitgroup for all goroutines
	// this ensures all goroutines are finished
	// when we call wg.Wait()
	var wg, progressGroup sync.WaitGroup

	wg.Add(1)
	go resultWorker(gobuster, opts.OutputFilename, output.stdoutWriter(), &wg)

	wg.Add(1)
	go errorWorker(gobuster, &wg)

	wg.Add(1)
	go messageWorker(gobuster, &wg)

	if !opts.Quiet && !opts.NoProgress {
		// if not quiet add a new workgroup entry and start the goroutine
		progressGroup.Add(1)
		go progressWorker(ctxCancel, gobuster, output, &progressGroup)
	}

	err = gobuster.Run(ctxCancel)

	// Drain results and messages before printing the final progress line.
	wg.Wait()
	cancel()
	progressGroup.Wait()

	// Late error checking to finish all threads
	if err != nil {
		return err
	}

	if !opts.Quiet {
		if summary, ok := plugin.(libgobuster.SummaryPlugin); ok {
			if err := summary.WriteSummary(output.stdoutWriter()); err != nil {
				return fmt.Errorf("write scan summary: %w", err)
			}
		}
		log.Println(ruler)
		gobuster.Logger.Println("Finished")
		log.Println(ruler)
	}
	return nil
}
