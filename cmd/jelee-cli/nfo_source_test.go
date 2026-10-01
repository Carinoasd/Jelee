package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
)

func TestNFOChangedSourceReportsSafeRetryableFailure(t *testing.T) {
	// Source errors may be wrapped by future task adapters. Their private
	// context must never become a diagnostic or an invalid-XML result.
	for _, cause := range []error{nfo.ErrChanged, fmt.Errorf("private/path/movie.nfo: %w", nfo.ErrChanged)} {
		code, exit := nfoFailure(cause)
		if code != "nfo_changed" || exit != 1 || strings.Contains(code, "private") {
			t.Fatalf("changed source diagnostic=%q exit=%d", code, exit)
		}
	}
	for _, tc := range []struct {
		cause error
		code  string
		exit  int
	}{
		{context.Canceled, "nfo_cancelled", 130},
		{context.DeadlineExceeded, "nfo_timeout", 124},
	} {
		code, exit := nfoFailure(errors.Join(nfo.ErrChanged, tc.cause))
		if code != tc.code || exit != tc.exit {
			t.Fatalf("cancellation lost priority: code=%q exit=%d", code, exit)
		}
	}
}
