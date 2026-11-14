package libgobuster

import (
	"context"
	"io"
)

// GobusterPlugin is an interface which plugins must implement
type GobusterPlugin interface {
	Name() string
	PreRun(context.Context, *Progress) error
	ProcessWord(context.Context, string, *Progress) (Result, error)
	AdditionalWords(string) []string
	AdditionalWordsLen() int
	AdditionalSuccessWords(string) []string
	GetConfigString() (string, error)
}

// MultiPassPlugin can prepare another wordlist pass after all work has drained.
// NextPass is never called concurrently with ProcessWord or AdditionalWords.
type MultiPassPlugin interface {
	NextPass(context.Context, *Progress) (bool, error)
}

// ReplayPlugin requests a replayable stdin wordlist before the first pass.
type ReplayPlugin interface {
	NeedsWordlistReplay() bool
}

// SummaryPlugin renders a final report after all results and progress output
// have drained. The CLI keeps this separate from the flat results file.
type SummaryPlugin interface {
	WriteSummary(io.Writer) error
}

// Result is an interface for the Result object
type Result interface {
	ResultToString() (string, error)
}
