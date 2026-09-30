package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
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
	return runNFO(ctx, argv, stdout, stderr)
}

// runNFO does not load service configuration or open a database. Only the
// validation summary is emitted; metadata text and local/remote paths stay out.
func runNFO(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: jelee-cli nfo validate --root ABSOLUTE_PATH --file RELATIVE/FILE.nfo [--max-bytes 8388608]")
		return 2
	}
	if len(argv) < 1 || argv[0] != "validate" {
		return usage()
	}
	args := flag.NewFlagSet("nfo validate", flag.ContinueOnError)
	args.SetOutput(io.Discard)
	root := args.String("root", "", "absolute metadata root")
	file := args.String("file", "", "root-relative NFO path using slash separators")
	maxBytes := args.Int64("max-bytes", nfo.DefaultMaxBytes, "maximum source bytes, at most 33554432")
	if err := args.Parse(argv[1:]); err != nil || args.NArg() != 0 || *root == "" || *file == "" || *maxBytes < 1 || *maxBytes > nfo.MaxAllowedBytes {
		return usage()
	}
	document, err := nfo.ReadFile(ctx, *root, *file, *maxBytes)
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
