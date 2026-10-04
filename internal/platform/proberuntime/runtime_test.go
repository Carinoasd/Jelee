package proberuntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/MoYuanCN/Jelee/tools"
)

type healthRunner func(context.Context, process.Request) (process.Result, error)

func (f healthRunner) Run(ctx context.Context, request process.Request) (process.Result, error) {
	return f(ctx, request)
}

func TestPolicyIsFixedProtectedAndFresh(t *testing.T) {
	policy, err := Policy()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		if err != ErrUnavailable || len(policy.Libraries) != 0 || policy.FFprobeSHA256 != "" {
			t.Fatal("unsupported policy enabled")
		}
		return
	}
	spec, specErr := tools.RuntimeSpec("linux-amd64")
	if err != nil || specErr != nil || !policy.RequireProtectedFiles || len(policy.Libraries) != 8 || FFprobePath != "/usr/lib/jelee/ffprobe" {
		t.Fatal("production policy unavailable or unprotected")
	}
	for i, library := range policy.Libraries {
		if library.Path != spec.Libraries[i].ContainerPath || library.SHA256 != spec.Libraries[i].SHA256 {
			t.Fatal("policy used host/local runtime identity")
		}
	}
	policy.Libraries[0].SHA256 = "changed"
	again, _ := Policy()
	if again.Libraries[0].SHA256 == "changed" {
		t.Fatal("policy mutable across callers")
	}
}

func TestHelperMalformedArgumentsNeverBecomeMediaRequest(t *testing.T) {
	want := sandbox.ExitInvalid
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		want = sandbox.ExitUnavailable
	}
	alternate := base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"mode":"metadata","ffprobePath":"/other/root-owned/ffprobe"}`))
	for _, argv := range [][]string{nil, {"invalid"}, {"secret-path", "extra"}, {alternate}} {
		if got := Helper(argv); got != want {
			t.Fatalf("helper code %d", got)
		}
	}
}

func TestNewRejectsNilCancelledOrUnregisteredHost(t *testing.T) {
	if runner, err := New(nil, t.TempDir()); err != ErrUnavailable || runner != nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if runner, err := New(ctx, t.TempDir()); err != ErrUnavailable || runner != nil {
		t.Fatal("cancelled factory accepted")
	}
}

func TestDiagnosticContextAndUnsupportedBeforeAnyIO(t *testing.T) {
	factory := func(context.Context, string) (diagnosticRunner, error) {
		t.Fatal("unexpected factory call")
		return nil, nil
	}
	if got := diagnose(nil, "linux-amd64", factory); got.Reason != "invalid_context" || got.Capability != "disabled" {
		t.Fatal(got)
	}
	if got := diagnose(context.Background(), "windows-amd64", factory); got.Reason != "platform_unsupported" || got.Capability != "disabled" {
		t.Fatal(got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := diagnose(ctx, "linux-amd64", factory); got.State != "cancelled" || got.Reason != "operation_cancelled" {
		t.Fatal(got)
	}
	deadline, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	if got := diagnose(deadline, "linux-amd64", factory); got.State != "timed_out" || got.Reason != "deadline_exceeded" {
		t.Fatal(got)
	}
}

func TestDiagnosticOnlyExpectedExitEnablesCapabilityAndDiscardsOutput(t *testing.T) {
	for _, resultErr := range []error{process.ErrExit, process.ErrUnexpectedExit, process.ErrSandboxInvalid, process.ErrSandboxUnavailable, process.ErrCleanup, process.ErrStart, process.ErrTimeout, process.ErrOutputLimit, errors.New("/private/source token"), nil} {
		var directory string
		factory := func(_ context.Context, dir string) (diagnosticRunner, error) {
			directory = dir
			return healthRunner(func(_ context.Context, request process.Request) (process.Result, error) {
				if request.Tool != "ffprobe" || request.Operation != "metadata" || request.Stdin == nil {
					t.Fatal("diagnostic request not fixed")
				}
				value, err := io.ReadAll(request.Stdin)
				if err != nil || string(value) != "Jelee sandbox health check; deliberately invalid media\n" {
					t.Fatal("invalid health sample")
				}
				if _, err := request.Stdin.WriteString("must remain read-only"); err == nil {
					t.Fatal("health input writable")
				}
				return process.Result{Stdout: []byte("private media metadata"), StdoutBytes: 22, StderrBytes: 8}, resultErr
			}), nil
		}
		got := diagnose(context.Background(), "linux-amd64", factory)
		if (got.State == "available") != (resultErr == process.ErrExit) {
			t.Fatalf("incorrect health classification %v: %+v", resultErr, got)
		}
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), directory) {
			t.Fatal("diagnostic leaked source/paths")
		}
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Fatal("diagnostic scratch retained")
		}
	}
}

func TestDiagnosticCancellationFactoryFailureAndInputCloseDisableCapability(t *testing.T) {
	for _, mode := range []string{"factory", "cancel-run", "cancel-factory", "closed-input"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var dir string
			got := diagnose(ctx, "linux-amd64", func(_ context.Context, directory string) (diagnosticRunner, error) {
				dir = directory
				if mode == "factory" {
					return nil, errors.New("private factory details")
				}
				if mode == "cancel-factory" {
					cancel()
					return nil, ErrUnavailable
				}
				return healthRunner(func(_ context.Context, request process.Request) (process.Result, error) {
					if mode == "cancel-run" {
						cancel()
					} else {
						_ = request.Stdin.Close()
					}
					return process.Result{}, process.ErrExit
				}), nil
			})
			if got.Capability != "disabled" || got.State == "available" {
				t.Fatal("failed health enabled capability")
			}
			if strings.HasPrefix(mode, "cancel") && got.State != "cancelled" {
				t.Fatal("cancellation lost")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("scratch leaked")
			}
		})
	}
}

func TestHelperAcceptsOnlyCanonicalShippedDescriptors(t *testing.T) {
	encode := func(text string) string { return base64.RawURLEncoding.EncodeToString([]byte(text)) }
	if !expectedDescriptor(encode(`{"version":1,"mode":"metadata","ffprobePath":"` + FFprobePath + `"}`)) {
		t.Fatal("metadata descriptor refused")
	}
	for stream := 0; stream <= sandbox.CoverMaxVideoIndex; stream++ {
		if !expectedDescriptor(encode(`{"version":1,"mode":"cover","ffprobePath":"` + FFprobePath + `","stream":` + strconv.Itoa(stream) + `}`)) {
			t.Fatal("cover descriptor refused", stream)
		}
	}
	for _, text := range []string{
		`{"version":1,"mode":"cover","ffprobePath":"` + FFprobePath + `","stream":16}`,
		`{"version":1,"mode":"cover","ffprobePath":"/other/ffprobe","stream":0}`,
		`{"version":1,"mode":"cover","ffprobePath":"` + FFprobePath + `","stream":01}`,
		`{"version":1,"mode":"cover","ffprobePath":"` + FFprobePath + `"}`,
	} {
		if expectedDescriptor(encode(text)) {
			t.Fatal("non-canonical descriptor accepted", text)
		}
		want := sandbox.ExitInvalid
		if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
			want = sandbox.ExitUnavailable
		}
		if got := Helper([]string{encode(text)}); got != want {
			t.Fatalf("helper code %d", got)
		}
	}
}

func TestNewCoverRejectsNilCancelledOrUnregisteredHost(t *testing.T) {
	if runner, err := NewCover(nil, t.TempDir()); err != ErrUnavailable || runner != nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if runner, err := NewCover(ctx, t.TempDir()); err != ErrUnavailable || runner != nil {
		t.Fatal("cancelled factory accepted")
	}
}
