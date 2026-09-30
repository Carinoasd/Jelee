package probe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

// Only same-package tests can replace the executor. The public constructor
// continues to require the concrete isolated runner.
type adapterExecutorFunc func(context.Context, process.Request) (process.Result, error)

func (f adapterExecutorFunc) Run(ctx context.Context, request process.Request) (process.Result, error) {
	return f(ctx, request)
}

func adapterFixture(t *testing.T) (Source, string, []byte) {
	t.Helper()
	root := t.TempDir()
	data := bytes.Repeat([]byte("original media\x00\xff"), 16384)
	name := writeInput(t, root, "folder/private movie.mp4", data)
	return Source{RootPath: root, RelativePath: "folder/private movie.mp4"}, name, data
}

func adapterOutput(size int) process.Result {
	return process.Result{Stdout: []byte(fmt.Sprintf(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":320,"height":180}],"format":{"size":"%d"}}`, size))}
}

func assertEmptyObservation(t *testing.T, got Observation, err, want error) {
	t.Helper()
	if !errors.Is(err, want) || err.Error() != want.Error() {
		t.Fatalf("error = %v, want fixed %v", err, want)
	}
	if !reflect.DeepEqual(got, Observation{}) {
		t.Fatal("failure returned candidate metadata or source identity")
	}
}

func TestNewAdapterRequiresIsolatedRunnerAndSafeIdentity(t *testing.T) {
	for _, identity := range []string{"", strings.Repeat("a", 257), "line\nbreak", "nul\x00", string([]byte{0xff})} {
		if got, err := NewAdapter(&process.IsolatedRunner{}, identity); got != nil || err != ErrInvalidInput {
			t.Fatalf("invalid identity accepted: %v", err)
		}
	}
	if got, err := NewAdapter(nil, "verified-tool"); got != nil || err != ErrInvalidInput {
		t.Fatal("nil isolated runner accepted")
	}
	if got, err := NewAdapter(&process.IsolatedRunner{}, "ffprobe-sha256:verified"); got == nil || err != nil {
		t.Fatal("valid constructor rejected")
	}
}

func TestAdapterNormalFixedReadOnlyFDAndUnchangedSource(t *testing.T) {
	source, name, data := adapterFixture(t)
	before, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	var passed *os.File
	adapter := &Adapter{identity: "verified-tool", runner: adapterExecutorFunc(func(ctx context.Context, request process.Request) (process.Result, error) {
		if ctx == nil || request.Tool != "ffprobe" || request.Operation != "metadata" || request.Stdin == nil {
			t.Fatal("request is not the fixed metadata operation")
		}
		passed = request.Stdin
		if offset, err := passed.Seek(0, io.SeekCurrent); err != nil || offset != 0 {
			t.Fatal("pre-probe fingerprint changed the FD offset")
		}
		if _, err := passed.Write([]byte("forbidden")); err == nil {
			t.Fatal("child input is writable")
		}
		got, err := io.ReadAll(passed)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("child received incorrect input")
		}
		return adapterOutput(len(data)), nil
	})}
	got, err := adapter.Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if got.File != (Metadata{Size: int64(len(data)), ModifiedUnixNano: before.ModTime().UnixNano()}) || got.FingerprintVersion != FingerprintVersion || len(got.Fingerprint) != 64 || got.ToolIdentity != "verified-tool" {
		t.Fatalf("wrong observation identity: %+v", got)
	}
	if len(got.Metadata.Streams) != 1 || got.Metadata.Streams[0].Video == nil || *got.Metadata.Streams[0].Video.Width != 320 || got.Metadata.Format.SizeBytes == nil || *got.Metadata.Format.SizeBytes != int64(len(data)) {
		t.Fatal("normalized metadata lost")
	}
	if _, err := passed.Stat(); err == nil {
		t.Fatal("input descriptor leaked after success")
	}
	after, err := os.ReadFile(name)
	if err != nil || sha256.Sum256(after) != sha256.Sum256(data) {
		t.Fatal("source bytes changed")
	}
	afterInfo, err := os.Stat(name)
	if err != nil || !matches(afterInfo, got.File) {
		t.Fatal("source stat changed")
	}
}

func TestAdapterProcessErrorsDiscardEvenValidOutput(t *testing.T) {
	cases := []struct {
		name        string
		input, want error
	}{
		{"sandbox unavailable", process.ErrSandboxUnavailable, ErrToolUnavailable},
		{"unsupported platform", process.ErrUnsupported, ErrToolUnavailable},
		{"start", process.ErrStart, ErrToolUnavailable},
		{"cancel", process.ErrCancelled, context.Canceled},
		{"context cancel", context.Canceled, context.Canceled},
		{"timeout", process.ErrTimeout, context.DeadlineExceeded},
		{"context timeout", context.DeadlineExceeded, context.DeadlineExceeded},
		{"busy", process.ErrBusy, process.ErrBusy},
		{"corrupt", process.ErrExit, ErrFailed},
		{"output limit", process.ErrOutputLimit, process.ErrOutputLimit},
		{"cleanup", process.ErrCleanup, ErrToolUnavailable},
		{"invalid descriptor", process.ErrSandboxInvalid, ErrToolUnavailable},
		{"invalid runner", process.ErrInvalid, ErrToolUnavailable},
		{"unexpected exit or signal", process.ErrUnexpectedExit, ErrToolUnavailable},
		{"unknown private error", errors.New("/private/library/movie token=secret"), ErrToolUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, _, data := adapterFixture(t)
			var passed *os.File
			adapter := &Adapter{identity: "verified", runner: adapterExecutorFunc(func(_ context.Context, request process.Request) (process.Result, error) {
				passed = request.Stdin
				return adapterOutput(len(data)), fmt.Errorf("private child stderr: %w", tc.input)
			})}
			got, err := adapter.Probe(context.Background(), source)
			assertEmptyObservation(t, got, err, tc.want)
			if _, err := passed.Stat(); err == nil {
				t.Fatal("failed child input descriptor leaked")
			}
		})
	}
}

func TestAdapterMalformedOutputIsDistinctFromChildFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output []byte
		want   error
	}{
		{"empty", nil, ErrMetadataInvalid},
		{"syntax", []byte("PRIVATE RAW DIAGNOSTIC"), ErrMetadataInvalid},
		{"empty document", []byte(`{}`), ErrMetadataInvalid},
		{"duplicate", []byte(`{"streams":[],"streams":[]}`), ErrMetadataInvalid},
		{"over limit", bytes.Repeat([]byte(" "), MaxJSONBytes+1), ErrMetadataLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, _, _ := adapterFixture(t)
			adapter := &Adapter{identity: "verified", runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) {
				return process.Result{Stdout: tc.output}, nil
			})}
			got, err := adapter.Probe(context.Background(), source)
			assertEmptyObservation(t, got, err, tc.want)
		})
	}
}

func TestAdapterInvalidInputsNeverCallRunner(t *testing.T) {
	source, _, _ := adapterFixture(t)
	adapter := &Adapter{runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) {
		t.Fatal("runner called before input validation")
		return process.Result{}, nil
	})}
	for _, tc := range []struct {
		name    string
		adapter *Adapter
		ctx     context.Context
		source  Source
		want    error
	}{
		{"nil adapter", nil, context.Background(), source, ErrInvalidInput},
		{"nil runner", &Adapter{}, context.Background(), source, ErrInvalidInput},
		{"nil context", adapter, nil, source, ErrInvalidInput},
		{"invalid path", adapter, context.Background(), Source{RootPath: source.RootPath, RelativePath: "../private"}, ErrInvalidInput},
		{"missing file", adapter, context.Background(), Source{RootPath: source.RootPath, RelativePath: "missing"}, ErrUnavailable},
		{"directory", adapter, context.Background(), Source{RootPath: source.RootPath, RelativePath: "folder"}, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.adapter.Probe(tc.ctx, tc.source)
			assertEmptyObservation(t, got, err, tc.want)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := adapter.Probe(ctx, source)
	assertEmptyObservation(t, got, err, context.Canceled)
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	got, err = adapter.Probe(ctx, source)
	assertEmptyObservation(t, got, err, context.DeadlineExceeded)
}

func TestAdapterCancellationOverridesSuccessfulChild(t *testing.T) {
	source, _, data := adapterFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var passed *os.File
	adapter := &Adapter{runner: adapterExecutorFunc(func(_ context.Context, request process.Request) (process.Result, error) {
		passed = request.Stdin
		cancel()
		return adapterOutput(len(data)), nil
	})}
	got, err := adapter.Probe(ctx, source)
	assertEmptyObservation(t, got, err, context.Canceled)
	if _, err := passed.Stat(); err == nil {
		t.Fatal("canceled input descriptor leaked")
	}
}

func TestAdapterRunningCancellationClosesParentInput(t *testing.T) {
	source, _, _ := adapterFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan *os.File, 1)
	adapter := &Adapter{runner: adapterExecutorFunc(func(ctx context.Context, request process.Request) (process.Result, error) {
		entered <- request.Stdin
		<-ctx.Done()
		return process.Result{}, process.ErrCancelled
	})}
	done := make(chan struct {
		observation Observation
		err         error
	}, 1)
	go func() {
		got, err := adapter.Probe(ctx, source)
		done <- struct {
			observation Observation
			err         error
		}{got, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("runner not reached")
	}
	cancel()
	select {
	case result := <-done:
		assertEmptyObservation(t, result.observation, result.err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled probe did not return")
	}
}

func TestAdapterContextTakesPriorityOverChildError(t *testing.T) {
	for _, childError := range []error{process.ErrExit, process.ErrCleanup, process.ErrBusy} {
		t.Run(childError.Error(), func(t *testing.T) {
			source, _, data := adapterFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			adapter := &Adapter{runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) {
				cancel()
				return adapterOutput(len(data)), childError
			})}
			got, err := adapter.Probe(ctx, source)
			assertEmptyObservation(t, got, err, context.Canceled)
		})
	}
}

func TestAdapterDeadlineDiscardsSuccessfulChildOutput(t *testing.T) {
	source, _, data := adapterFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	adapter := &Adapter{runner: adapterExecutorFunc(func(ctx context.Context, request process.Request) (process.Result, error) {
		<-ctx.Done()
		return adapterOutput(len(data)), nil
	})}
	got, err := adapter.Probe(ctx, source)
	assertEmptyObservation(t, got, err, context.DeadlineExceeded)
}

func TestAdapterRejectsChangedSource(t *testing.T) {
	for _, mode := range []string{"size", "mtime", "first edge", "last edge", "replace inode", "remove", "replace directory", "replace symlink", "container size", "closed handle"} {
		t.Run(mode, func(t *testing.T) {
			source, name, data := adapterFixture(t)
			before, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			want := ErrChanged
			if mode == "closed handle" {
				want = ErrUnavailable
			}
			adapter := &Adapter{runner: adapterExecutorFunc(func(_ context.Context, request process.Request) (process.Result, error) {
				switch mode {
				case "size":
					if err := os.Truncate(name, int64(len(data)-1)); err != nil {
						t.Fatal(err)
					}
				case "mtime":
					stamp := before.ModTime().Add(2 * time.Second)
					if err := os.Chtimes(name, stamp, stamp); err != nil {
						t.Fatal(err)
					}
				case "first edge", "last edge":
					file, err := os.OpenFile(name, os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					offset := int64(0)
					if mode == "last edge" {
						offset = int64(len(data) - 1)
					}
					if _, err := file.WriteAt([]byte("X"), offset); err != nil {
						_ = file.Close()
						t.Fatal(err)
					}
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(name, before.ModTime(), before.ModTime()); err != nil {
						t.Fatal(err)
					}
				case "replace inode":
					if err := os.Rename(name, name+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(name, data, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(name, before.ModTime(), before.ModTime()); err != nil {
						t.Fatal(err)
					}
				case "remove":
					if err := os.Remove(name); err != nil {
						t.Fatal(err)
					}
				case "replace directory":
					if err := os.Rename(name, name+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(name, 0700); err != nil {
						t.Fatal(err)
					}
				case "replace symlink":
					// Verify symlink support before changing the target path.
					link := filepath.Join(source.RootPath, "link-check")
					if err := os.Symlink(name, link); err != nil {
						t.Skip("host cannot create symlinks")
					}
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(name, name+".old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(name+".old", name); err != nil {
						t.Fatal(err)
					}
				case "container size":
					return adapterOutput(len(data) + 1), nil
				case "closed handle":
					if err := request.Stdin.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return adapterOutput(len(data)), nil
			})}
			got, err := adapter.Probe(context.Background(), source)
			assertEmptyObservation(t, got, err, want)
		})
	}
}

func TestAdapterQuickFingerprintIsNotFullSnapshot(t *testing.T) {
	source, name, data := adapterFixture(t)
	before, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) {
		file, err := os.OpenFile(name, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteAt([]byte("X"), int64(len(data)/2)); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, before.ModTime(), before.ModTime()); err != nil {
			t.Fatal(err)
		}
		return adapterOutput(len(data)), nil
	})}
	// Deliberately records the documented limit: unchanged inode, size, mtime
	// and sampled edges cannot establish that all media bytes stayed constant.
	if _, err := adapter.Probe(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(name)
	if err != nil || sha256.Sum256(after) == sha256.Sum256(data) {
		t.Fatal("fixture did not change its unsampled middle")
	}
}
