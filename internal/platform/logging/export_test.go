package logging

import (
	"log/slog"
	"testing"
)

// Hooks for the external tests of this directory, which link the OTLP
// sender (forward_otlp_scan_test.go).

// SensitiveSample is one G46.5 scan value and the fragments that must never
// appear in any output.
type SensitiveSample struct {
	Value   string
	Needles []string
}

// SensitiveSamples returns the scan values with the needles that are
// forbidden under the default redaction.
func SensitiveSamples() []SensitiveSample {
	out := make([]SensitiveSample, len(sensitiveSamples))
	for i, s := range sensitiveSamples {
		out[i] = SensitiveSample{Value: s.value, Needles: append(append([]string(nil), s.needles...), defaultOnlyNeedles...)}
	}
	return out
}

// InjectSensitive logs value under every scanned key and shape.
func InjectSensitive(log *slog.Logger, value string) { inject(log, value) }

// AssertNoLeak fails when output contains a needle.
func AssertNoLeak(t *testing.T, destination, output string, needles []string) {
	t.Helper()
	assertNoLeak(t, destination, output, needles)
}
