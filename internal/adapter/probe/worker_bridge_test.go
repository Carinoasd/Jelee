package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

func bridgeIdentity() domain.ProbeIdentity {
	return domain.ProbeIdentity{
		Platform: "linux-amd64", VendorVersion: "vendor-1", UpstreamVersion: "9.0.2",
		SourceRevision: strings.Repeat("a", 40), ExecutableSHA256: strings.Repeat("b", 64),
		RuntimeSHA256: strings.Repeat("c", 64), ArgumentsSHA256: strings.Repeat("d", 64),
		ParserVersion: domain.ProbeParserVersion, MetadataSchemaVersion: domain.ProbeMetadataSchemaVersion,
		SandboxVersion: "sandbox-v1", FingerprintVersion: domain.ProbeFingerprintVersion,
	}
}

func newTestBridge(t *testing.T, run adapterExecutorFunc) *WorkerBridge {
	t.Helper()
	identity := bridgeIdentity()
	digest, err := domain.ProbeIdentityDigest(identity)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewWorkerBridge(&Adapter{runner: run, identity: digest}, identity)
	if err != nil {
		t.Fatal(err)
	}
	return bridge
}

func assertEmptyWorkerObservation(t *testing.T, got domain.ProbeObservation, err, want error) {
	t.Helper()
	if err != want {
		t.Fatalf("error = %v, want fixed %v", err, want)
	}
	if !reflect.DeepEqual(got, domain.ProbeObservation{}) {
		t.Fatal("failure returned metadata or source identity")
	}
}

func TestWorkerBridgeConstructionRequiresMatchingTrustedIdentity(t *testing.T) {
	identity := bridgeIdentity()
	digest, err := domain.ProbeIdentityDigest(identity)
	if err != nil {
		t.Fatal(err)
	}
	run := adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) {
		return process.Result{}, process.ErrExit
	})
	invalidIdentity := identity
	invalidIdentity.ExecutableSHA256 = "unverified"
	for _, tc := range []struct {
		name     string
		adapter  *Adapter
		identity domain.ProbeIdentity
		want     error
	}{
		{"nil adapter", nil, identity, domain.ErrProbeRuntimeUnavailable},
		{"nil runner", &Adapter{identity: digest}, identity, domain.ErrProbeRuntimeUnavailable},
		{"invalid identity", &Adapter{runner: run, identity: digest}, invalidIdentity, domain.ErrProbeIdentityMismatch},
		{"different registration", &Adapter{runner: run, identity: strings.Repeat("e", 64)}, identity, domain.ErrProbeIdentityMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := NewWorkerBridge(tc.adapter, tc.identity); got != nil || err != tc.want {
				t.Fatalf("constructor = %v, %v", got, err)
			}
		})
	}
	adapter := &Adapter{runner: run, identity: digest}
	bridge, err := NewWorkerBridge(adapter, identity)
	if err != nil {
		t.Fatal(err)
	}
	identity.VendorVersion = "replaced"
	*adapter = Adapter{}
	if bridge.IdentityDigest() != digest || bridge.adapter.runner == nil || bridge.adapter.identity != digest {
		t.Fatal("caller replacement changed the bridge registration")
	}
	var missing *WorkerBridge
	if missing.IdentityDigest() != "" {
		t.Fatal("nil bridge returned an identity")
	}
}

func TestWorkerBridgeInspectAndProbeUseSameStampAndFixedReadonlyInput(t *testing.T) {
	source, name, original := adapterFixture(t)
	var passed *os.File
	calls := 0
	bridge := newTestBridge(t, func(ctx context.Context, request process.Request) (process.Result, error) {
		calls++
		if ctx == nil || request.Tool != "ffprobe" || request.Operation != "metadata" || request.Stdin == nil {
			t.Fatal("bridge changed fixed metadata operation")
		}
		passed = request.Stdin
		if offset, err := passed.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
			t.Fatal("Inspect/fingerprint moved input offset")
		}
		if _, err := passed.Write([]byte("forbidden")); err == nil {
			t.Fatal("writable input")
		}
		got, err := io.ReadAll(passed)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatal("incorrect source descriptor")
		}
		return adapterOutput(len(original)), nil
	})
	stamp, err := bridge.Inspect(context.Background(), source)
	if err != nil || calls != 0 {
		t.Fatalf("Inspect = %+v, %v; calls %d", stamp, err, calls)
	}
	got, err := bridge.Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.Stamp != stamp || got.IdentityDigest != bridge.IdentityDigest() {
		t.Fatal("worker result changed source stamp or registered identity")
	}
	if len(got.Metadata.Streams) != 1 || got.Metadata.Streams[0].Video == nil || *got.Metadata.Streams[0].Video.Width != 320 || *got.Metadata.Format.SizeBytes != int64(len(original)) {
		t.Fatal("normalized metadata lost")
	}
	if _, err := passed.Stat(); err == nil {
		t.Fatal("worker input descriptor leaked")
	}
	current, err := os.ReadFile(name)
	if err != nil || sha256.Sum256(current) != sha256.Sum256(original) {
		t.Fatal("original bytes changed")
	}
	after, err := bridge.Inspect(context.Background(), source)
	if err != nil || after != stamp || calls != 1 {
		t.Fatal("successful probe changed stat, stamp, or process count")
	}
}

func TestWorkerBridgeRuntimeFailuresNeverBecomeNegativeCacheResults(t *testing.T) {
	for _, input := range []error{
		process.ErrStart, process.ErrSandboxUnavailable, process.ErrSandboxInvalid,
		process.ErrUnsupported, process.ErrCleanup, process.ErrInvalid, process.ErrUnexpectedExit,
		errors.New("/private/library/movie token=secret unknown executor failure"),
	} {
		t.Run(fmt.Sprintf("runtime_%d", len(input.Error())), func(t *testing.T) {
			source, _, data := adapterFixture(t)
			bridge := newTestBridge(t, func(context.Context, process.Request) (process.Result, error) {
				return adapterOutput(len(data)), fmt.Errorf("private raw stderr: %w", input)
			})
			got, err := bridge.Probe(context.Background(), source)
			assertEmptyWorkerObservation(t, got, err, domain.ErrProbeRuntimeUnavailable)
		})
	}
}

func TestWorkerBridgeFixedFailureClassesDiscardAllOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  error
		output []byte
		want   error
	}{
		{"verified corrupt", process.ErrExit, nil, domain.ErrProbeFailed},
		{"busy", process.ErrBusy, nil, domain.ErrProbeBusy},
		{"output limit", process.ErrOutputLimit, nil, domain.ErrProbeOutputLimit},
		{"child canceled", process.ErrCancelled, nil, context.Canceled},
		{"child deadline", process.ErrTimeout, nil, context.DeadlineExceeded},
		{"empty JSON", nil, []byte{}, domain.ErrProbeMetadataInvalid},
		{"malformed JSON", nil, []byte("private/path diagnostics"), domain.ErrProbeMetadataInvalid},
		{"oversized JSON", nil, bytes.Repeat([]byte(" "), MaxJSONBytes+1), domain.ErrProbeMetadataLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, _, data := adapterFixture(t)
			var passed *os.File
			bridge := newTestBridge(t, func(_ context.Context, request process.Request) (process.Result, error) {
				passed = request.Stdin
				if tc.output != nil {
					return process.Result{Stdout: tc.output}, nil
				}
				return adapterOutput(len(data)), tc.input
			})
			ctx := context.Background()
			got, err := bridge.Probe(ctx, source)
			assertEmptyWorkerObservation(t, got, err, tc.want)
			if ctx.Err() != nil {
				t.Fatal("per-file error canceled caller context")
			}
			if _, err := passed.Stat(); err == nil {
				t.Fatal("failed input descriptor leaked")
			}
		})
	}
}

func TestWorkerBridgeFailuresBeforeExecutionReturnZero(t *testing.T) {
	source, _, _ := adapterFixture(t)
	bridge := newTestBridge(t, func(context.Context, process.Request) (process.Result, error) {
		t.Fatal("invalid input started process")
		return process.Result{}, nil
	})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	for _, tc := range []struct {
		name   string
		bridge *WorkerBridge
		ctx    context.Context
		source Source
		want   error
	}{
		{"nil bridge", nil, context.Background(), source, domain.ErrProbeRuntimeUnavailable},
		{"zero bridge", &WorkerBridge{}, context.Background(), source, domain.ErrProbeRuntimeUnavailable},
		{"nil context", bridge, nil, source, domain.ErrProbeRuntimeUnavailable},
		{"canceled", bridge, cancelled, source, context.Canceled},
		{"expired", bridge, expired, source, context.DeadlineExceeded},
		{"missing", bridge, context.Background(), Source{RootPath: source.RootPath, RelativePath: "missing"}, domain.ErrProbeInputUnavailable},
		{"invalid", bridge, context.Background(), Source{RootPath: source.RootPath, RelativePath: "../private"}, domain.ErrProbeInputUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stamp, err := tc.bridge.Inspect(tc.ctx, tc.source)
			if err != tc.want || stamp != (domain.ProbeStamp{}) {
				t.Fatalf("Inspect error %v; stamp %+v", err, stamp)
			}
			got, err := tc.bridge.Probe(tc.ctx, tc.source)
			assertEmptyWorkerObservation(t, got, err, tc.want)
		})
	}
}

func TestWorkerBridgeCancellationOverridesChildResult(t *testing.T) {
	for _, childErr := range []error{nil, process.ErrExit, process.ErrUnexpectedExit} {
		t.Run(fmt.Sprint(childErr), func(t *testing.T) {
			source, _, data := adapterFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var passed *os.File
			bridge := newTestBridge(t, func(_ context.Context, request process.Request) (process.Result, error) {
				passed = request.Stdin
				cancel()
				return adapterOutput(len(data)), childErr
			})
			got, err := bridge.Probe(ctx, source)
			assertEmptyWorkerObservation(t, got, err, context.Canceled)
			if _, err := passed.Stat(); err == nil {
				t.Fatal("canceled input descriptor leaked")
			}
		})
	}
}

func TestWorkerBridgeChangedSourceIsNotSuccessOrBadMedia(t *testing.T) {
	source, name, data := adapterFixture(t)
	bridge := newTestBridge(t, func(context.Context, process.Request) (process.Result, error) {
		if err := os.Truncate(name, int64(len(data)-1)); err != nil {
			t.Fatal(err)
		}
		return adapterOutput(len(data)), nil
	})
	got, err := bridge.Probe(context.Background(), source)
	assertEmptyWorkerObservation(t, got, err, domain.ErrProbeSourceChanged)
}

func TestWorkerObservationRejectsInvalidTrustedResult(t *testing.T) {
	metadata, err := ParseJSON(adapterOutput(12).Stdout)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("d", 64)
	valid := Observation{Metadata: metadata, File: Metadata{Size: 12, ModifiedUnixNano: 100}, Fingerprint: strings.Repeat("a", 64), FingerprintVersion: FingerprintVersion, ToolIdentity: digest}
	for _, tc := range []struct {
		name   string
		mutate func(*Observation, *string)
		want   error
	}{
		{"different tool", func(v *Observation, _ *string) { v.ToolIdentity = strings.Repeat("b", 64) }, domain.ErrProbeIdentityMismatch},
		{"empty identity", func(_ *Observation, digest *string) { *digest = "" }, domain.ErrProbeIdentityMismatch},
		{"invalid stamp", func(v *Observation, _ *string) { v.Fingerprint = "invalid" }, domain.ErrProbeRuntimeUnavailable},
		{"invalid metadata", func(v *Observation, _ *string) { v.Metadata = domain.MediaMetadata{} }, domain.ErrProbeMetadataInvalid},
		{"different size", func(v *Observation, _ *string) { v.File.Size++ }, domain.ErrProbeSourceChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, key := valid, digest
			tc.mutate(&v, &key)
			got, err := workerObservation(v, key)
			assertEmptyWorkerObservation(t, got, err, tc.want)
		})
	}
}

func TestWorkerErrorSanitizesWrappedErrors(t *testing.T) {
	if workerError(nil) != nil {
		t.Fatal("nil error changed")
	}
	for _, tc := range []struct{ input, want error }{
		{context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded},
		{ErrInvalidInput, domain.ErrProbeInputUnavailable}, {ErrUnavailable, domain.ErrProbeInputUnavailable},
		{ErrChanged, domain.ErrProbeSourceChanged}, {ErrMetadataInvalid, domain.ErrProbeMetadataInvalid},
		{ErrMetadataLimit, domain.ErrProbeMetadataLimit}, {process.ErrOutputLimit, domain.ErrProbeOutputLimit},
		{process.ErrBusy, domain.ErrProbeBusy}, {ErrFailed, domain.ErrProbeFailed}, {process.ErrExit, domain.ErrProbeFailed},
		{process.ErrCancelled, context.Canceled}, {process.ErrTimeout, context.DeadlineExceeded},
		{errors.New("private-path token=secret"), domain.ErrProbeRuntimeUnavailable},
	} {
		if got := workerError(fmt.Errorf("untrusted detail: %w", tc.input)); got != tc.want {
			t.Fatalf("classification %v, want fixed %v", got, tc.want)
		}
	}
}
