package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
)

type nfoValidation struct {
	Valid         bool        `json:"valid"`
	Root          string      `json:"root"`
	Encoding      string      `json:"encoding"`
	OriginalBytes int64       `json:"originalBytes"`
	Entries       int         `json:"entries"`
	Issues        []nfo.Issue `json:"issues"`
}

// The process owns stdout. Closing it on cancellation releases a blocked pipe
// write when the receiving command stops reading. Library callers of runNFO
// retain ownership of their writer and must provide their own write deadlines.
func runNFOWithOutputCancellation(ctx context.Context, argv []string, stdout io.WriteCloser, stderr io.Writer) int {
	return runNFOCLIWithOutputCancellation(ctx, argv, nil, stdout, stderr)
}

func runNFOCLIWithOutputCancellation(ctx context.Context, argv []string, stdin io.Reader, stdout io.WriteCloser, stderr io.Writer) int {
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(closed)
		_ = stdout.Close()
	})
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	return runNFOCLI(ctx, argv, stdin, stdout, stderr)
}

// Retain the original no-stdin entry point for local validation callers. The
// unified dispatcher still rejects remote commands without a token on stdin.
func runNFO(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	return runNFOCLI(ctx, argv, nil, stdout, stderr)
}

// Local validation never loads service configuration or opens a database.
func runNFOLocal(ctx context.Context, root, file string, maxBytes int64, stdout, stderr io.Writer) int {
	document, err := nfo.ReadFile(ctx, root, file, maxBytes)
	if err != nil {
		code, exit := nfoFailure(err)
		fmt.Fprintln(stderr, code)
		return exit
	}
	issues := document.Validate()
	if issues == nil {
		issues = []nfo.Issue{}
	}
	valid := true
	for _, issue := range issues {
		if issue.Severity == "error" {
			valid = false
		}
	}
	rootKind := "unknown"
	switch document.Root {
	case "movie", "tvshow", "season", "episode", "episodedetails":
		rootKind = document.Root
	}
	result := nfoValidation{valid, rootKind, document.Encoding, document.OriginalSize, len(document.Entries), issues}
	if err := ctx.Err(); err != nil {
		code, exit := nfoFailure(err)
		fmt.Fprintln(stderr, code)
		return exit
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		if ctx.Err() != nil {
			code, exit := nfoFailure(ctx.Err())
			fmt.Fprintln(stderr, code)
			return exit
		}
		fmt.Fprintln(stderr, "nfo_output_failed")
		return 1
	}
	if !valid {
		return 3
	}
	return 0
}

func nfoFailure(err error) (string, int) {
	if errors.Is(err, context.Canceled) {
		return "nfo_cancelled", 130
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "nfo_timeout", 124
	}
	for _, expected := range []error{nfo.ErrInvalidInput, nfo.ErrTooLarge, nfo.ErrTooComplex, nfo.ErrInvalidXML, nfo.ErrUnsafeXML, nfo.ErrInvalidEncoding, nfo.ErrUnsupportedEncoding, nfo.ErrRead, nfo.ErrNotFound, nfo.ErrChanged} {
		if errors.Is(err, expected) {
			return expected.Error(), 1
		}
	}
	return "nfo_validation_failed", 1
}
