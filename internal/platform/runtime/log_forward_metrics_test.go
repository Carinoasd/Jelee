package runtime

import (
	"io"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/telemetry"
)

// The router feeds the destination counters to telemetry through the
// adapter; a destination keeps its configured name.
func TestLogCounterSourceExposesForwarderDestinations(t *testing.T) {
	r, err := logging.Open(logging.Options{Forward: []logging.ForwarderOptions{
		{Kind: logging.ForwardSyslog, Name: "central", Syslog: logging.SyslogOptions{Address: "127.0.0.1:9"}},
	}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	var source telemetry.LogForwardCounters = logCounterSource{r}
	stats := source.ForwardStats()
	if len(stats) != 1 || stats[0].Destination != "central" {
		t.Fatalf("stats %+v", stats)
	}
	var _ telemetry.LogCounters = logCounterSource{r}
}
