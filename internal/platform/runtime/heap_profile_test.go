//go:build jelee_probe_tests

package runtime

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

const heapProfileDirectory = "/tmp/jelee-heap-profiles"
const heapProfileMaxBytes int64 = 8 << 20
const heapProfileExportCommand = "--export-heap-profile"
const heapProfileExportFailure = "heap_profile_export_failed"

var errHeapProfileArtifact = errors.New("heap profile artifact could not be published")

// The export child has no test runner or product initialization. Its only
// successful output is the fixed artifact's binary stream. Docker's client
// timeout alone would not guarantee the child exits after stdout is blocked.
func heapProfileExportMain(args []string) int {
	prepareHeapProfileExport()
	watchdog := time.AfterFunc(20*time.Second, func() {
		// Do not let a blocked stderr prevent the independent exit deadline.
		os.Exit(1)
	})
	defer watchdog.Stop()
	return heapProfileExportCode(args, os.Stdout, os.Stderr, func(stage string, out io.Writer) error {
		return exportHeapProfile(heapProfileDirectory, stage, out)
	})
}

func heapProfileExportCode(args []string, out, stderr io.Writer, export func(string, io.Writer) error) int {
	if len(args) != 2 || args[0] != heapProfileExportCommand || args[1] != "before" && args[1] != "after" {
		_, _ = io.WriteString(stderr, heapProfileExportFailure+"\n")
		return 1
	}
	if err := export(args[1], out); err != nil {
		_, _ = io.WriteString(stderr, heapProfileExportFailure+"\n")
		return 1
	}
	return 0
}

func streamHeapProfile(out io.Writer, source io.Reader, size int64) error {
	if size <= 0 || size > heapProfileMaxBytes {
		return errors.New(heapProfileExportFailure)
	}
	if n, err := io.CopyN(out, source, size); err != nil || n != size {
		return errors.New(heapProfileExportFailure)
	}
	var extra [1]byte
	if n, err := source.Read(extra[:]); n != 0 || err != io.EOF {
		return errors.New(heapProfileExportFailure)
	}
	return nil
}

type heapProfileShortWriter struct{}

func (heapProfileShortWriter) Write(data []byte) (int, error) { return len(data) / 2, nil }

func TestHeapProfileExportStreamRequiresExactSize(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int64
		body string
	}{
		{"exact", 7, "profile"}, {"empty", 0, ""}, {"negative", -1, ""},
		{"oversized", heapProfileMaxBytes + 1, ""}, {"truncated", 8, "profile"}, {"extra", 6, "profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			err := streamHeapProfile(&output, strings.NewReader(tc.body), tc.size)
			if (err == nil) != (tc.name == "exact") || tc.name == "exact" && output.String() != tc.body {
				t.Fatal("heap export did not require an exact bounded stream", err)
			}
		})
	}
	if err := streamHeapProfile(heapProfileShortWriter{}, strings.NewReader("profile"), 7); err == nil {
		t.Fatal("heap export accepted a short stdout write")
	}
}

func TestHeapProfileExportCommandRejectsArgumentsWithoutRunningTests(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("test executable unavailable")
	}
	for _, args := range [][]string{
		{heapProfileExportCommand}, {heapProfileExportCommand, ""},
		{heapProfileExportCommand, "../private"}, {heapProfileExportCommand, "before", "extra"},
		{heapProfileExportCommand + "=before"}, {"-test.run=^TestDoesNotExist$", heapProfileExportCommand, "before"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		command := exec.CommandContext(ctx, executable, args...)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		cancel()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || stdout.Len() != 0 || stderr.String() != heapProfileExportFailure+"\n" {
			t.Fatal("invalid heap export arguments entered the test runner or leaked diagnostics")
		}
	}
}

func TestHeapProfileExportCommandWritesOnlyBinaryOrFixedError(t *testing.T) {
	for _, stage := range []string{"before", "after"} {
		var output, stderr bytes.Buffer
		data := []byte{0x1f, 0x8b, 0, 1, 2, 0xff}
		code := heapProfileExportCode([]string{heapProfileExportCommand, stage}, &output, &stderr, func(got string, out io.Writer) error {
			if got != stage {
				t.Fatal("export stage changed")
			}
			_, err := out.Write(data)
			return err
		})
		if code != 0 || stderr.Len() != 0 || !bytes.Equal(output.Bytes(), data) {
			t.Fatal("heap export changed binary stdout or wrote successful diagnostics")
		}
	}
	var output, stderr bytes.Buffer
	code := heapProfileExportCode([]string{heapProfileExportCommand, "before"}, &output, &stderr, func(string, io.Writer) error {
		return errors.New("PRIVATE_SOURCE_DETAIL")
	})
	if code != 1 || output.Len() != 0 || stderr.String() != heapProfileExportFailure+"\n" {
		t.Fatal("heap export exposed a source error")
	}
}

type heapProfileMetadata struct {
	Version        int    `json:"version"`
	Stage          string `json:"stage"`
	Bytes          int64  `json:"bytes"`
	SHA256         string `json:"sha256"`
	MemProfileRate int    `json:"memProfileRate"`
	NumGC          uint32 `json:"numGC"`
	ElapsedNanos   int64  `json:"elapsedNanos"`
}

type heapProfileArtifact struct {
	bytes  int64
	sha256 string
}

// These seams exercise artifact and handshake failures without starting the
// mixed worker. Native acceptance always uses the fixed directory and pprof.
type heapProfileCapture struct {
	directory string
	write     func(string, string) (heapProfileArtifact, error)
	emit      func(heapProfileMetadata) error
	ack       func(context.Context, string, heapProfileMetadata) error
	stats     func() (int, uint32)
}

func (profile *memoryProfileReport) captureHeap(t *testing.T, ctx context.Context, controlDir, stage string) {
	t.Helper()
	if profile == nil {
		return
	}
	hooks := heapProfileCapture{
		directory: heapProfileDirectory,
		write: func(directory, stage string) (heapProfileArtifact, error) {
			return writeHeapProfileArtifact(directory, stage, pprof.WriteHeapProfile, func(path string) (io.WriteCloser, error) {
				return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			}, os.Rename)
		},
		emit: func(metadata heapProfileMetadata) error {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"heapProfileReady": metadata})
		},
		ack: waitHeapProfileAck,
		stats: func() (int, uint32) {
			var stats goruntime.MemStats
			goruntime.ReadMemStats(&stats)
			return goruntime.MemProfileRate, stats.NumGC
		},
	}
	if err := profile.captureHeapWith(ctx, controlDir, stage, hooks); err != nil {
		t.Fatal("heap profile evidence failed", err)
	}
}

func (profile *memoryProfileReport) captureHeapWith(ctx context.Context, controlDir, stage string, hooks heapProfileCapture) error {
	if ctx.Err() != nil {
		return errors.New("heap profile capture cancelled")
	}
	if len(profile.HeapProfiles) > 1 || len(profile.HeapProfiles) == 0 && stage != "before" || len(profile.HeapProfiles) == 1 && stage != "after" {
		return errors.New("heap profile stages must be before then after")
	}
	rate, numGC := hooks.stats()
	if rate <= 0 || len(profile.HeapProfiles) == 1 && rate != profile.HeapProfiles[0].MemProfileRate {
		return errors.New("heap profile sampling rate is unavailable or changed")
	}
	metadata := heapProfileMetadata{Version: 1, Stage: stage, MemProfileRate: rate, NumGC: numGC, ElapsedNanos: time.Since(profile.started).Nanoseconds()}
	if metadata.ElapsedNanos < 0 || len(profile.HeapProfiles) == 1 && (metadata.ElapsedNanos <= profile.HeapProfiles[0].ElapsedNanos || numGC < profile.HeapProfiles[0].NumGC) {
		return errors.New("heap profile counters moved backwards")
	}
	if stage == "before" {
		// A pre-existing directory is rejected, including a symlink. This
		// process owns both fixed filenames for the lifetime of this container.
		if err := os.Mkdir(hooks.directory, 0700); err != nil {
			return errHeapProfileArtifact
		}
	}
	artifact, err := hooks.write(hooks.directory, stage)
	if err != nil {
		return errHeapProfileArtifact
	}
	metadata.Bytes, metadata.SHA256 = artifact.bytes, artifact.sha256
	profile.HeapProfiles = append(profile.HeapProfiles, metadata)
	if err := hooks.emit(metadata); err != nil {
		return errors.New("heap profile ready event could not be written")
	}
	return hooks.ack(ctx, controlDir, metadata)
}

type heapProfileLimitWriter struct {
	dst   io.Writer
	hash  hash.Hash
	bytes int64
	err   error
}

func (writer *heapProfileLimitWriter) Write(data []byte) (int, error) {
	if writer.err != nil {
		return 0, writer.err
	}
	if int64(len(data)) > heapProfileMaxBytes-writer.bytes {
		writer.err = errHeapProfileArtifact
		return 0, writer.err
	}
	n, err := writer.dst.Write(data)
	if n < 0 || n > len(data) {
		writer.err = errHeapProfileArtifact
		return 0, writer.err
	}
	writer.bytes += int64(n)
	_, _ = writer.hash.Write(data[:n])
	if err != nil || n != len(data) {
		writer.err = errHeapProfileArtifact
	}
	return n, writer.err
}

func writeHeapProfileArtifact(directory, stage string, producer func(io.Writer) error, open func(string) (io.WriteCloser, error), rename func(string, string) error) (heapProfileArtifact, error) {
	if stage != "before" && stage != "after" {
		return heapProfileArtifact{}, errHeapProfileArtifact
	}
	finalPath := filepath.Join(directory, stage+".heap.pb.gz")
	partialPath := finalPath + ".partial"
	if _, err := os.Lstat(finalPath); !errors.Is(err, os.ErrNotExist) {
		return heapProfileArtifact{}, errHeapProfileArtifact
	}
	file, err := open(partialPath)
	if err != nil {
		return heapProfileArtifact{}, errHeapProfileArtifact
	}
	closed, published := false, false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if !published {
			_ = os.Remove(partialPath)
		}
	}()
	writer := &heapProfileLimitWriter{dst: file, hash: sha256.New()}
	writeErr := producer(writer)
	closeErr := file.Close()
	closed = true
	if writeErr != nil || writer.err != nil || closeErr != nil || writer.bytes == 0 {
		return heapProfileArtifact{}, errHeapProfileArtifact
	}
	if err := rename(partialPath, finalPath); err != nil {
		return heapProfileArtifact{}, errHeapProfileArtifact
	}
	published = true
	return heapProfileArtifact{bytes: writer.bytes, sha256: hex.EncodeToString(writer.hash.Sum(nil))}, nil
}

func waitHeapProfileAck(ctx context.Context, controlDir string, metadata heapProfileMetadata) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return errors.New("heap profile copy acknowledgement timed out or was cancelled")
		}
		acknowledged, err := readHeapProfileAck(filepath.Join(controlDir, "heap-"+metadata.Stage+"-copied"), metadata.SHA256)
		if err != nil {
			return err
		}
		if acknowledged {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("heap profile copy acknowledgement timed out or was cancelled")
		case <-ticker.C:
		}
	}
}

func readHeapProfileAck(path, digest string) (bool, error) {
	invalid := errors.New("invalid heap profile copy acknowledgement")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() != 64 {
		return false, invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return false, invalid
	}
	opened, statErr := file.Stat()
	data, readErr := io.ReadAll(io.LimitReader(file, 65))
	closeErr := file.Close()
	if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || len(data) != 64 || string(data) != digest {
		return false, invalid
	}
	return true, nil
}

type heapProfileTestFile struct {
	*os.File
	failWrite bool
	failClose bool
	short     bool
}

func (file *heapProfileTestFile) Write(data []byte) (int, error) {
	if file.failWrite {
		return 0, errors.New("PRIVATE_WRITE_ERROR")
	}
	if file.short {
		return file.File.Write(data[:len(data)/2])
	}
	return file.File.Write(data)
}

func (file *heapProfileTestFile) Close() error {
	err := file.File.Close()
	if file.failClose {
		return errors.New("PRIVATE_CLOSE_ERROR")
	}
	return err
}

func TestHeapProfileWriterEnforcesExactLimit(t *testing.T) {
	writer := &heapProfileLimitWriter{dst: io.Discard, hash: sha256.New()}
	chunk := make([]byte, 64<<10)
	for i := 0; i < 128; i++ {
		if n, err := writer.Write(chunk); err != nil || n != len(chunk) {
			t.Fatal("heap profile writer rejected data within its exact limit")
		}
	}
	if writer.bytes != heapProfileMaxBytes {
		t.Fatal("heap profile writer recorded a different byte count")
	}
	if n, err := writer.Write([]byte{1}); err == nil || n != 0 || writer.bytes != heapProfileMaxBytes {
		t.Fatal("heap profile writer accepted a byte beyond its limit")
	}
}

func TestHeapProfileArtifactRejectsIncompletePublication(t *testing.T) {
	for _, mode := range []string{"write", "short", "close", "producer", "oversized", "rename", "empty"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			var renameCalls int
			artifact, err := writeHeapProfileArtifact(directory, "before", func(writer io.Writer) error {
				if mode == "producer" {
					return errors.New("PRIVATE_PRODUCER_ERROR")
				}
				if mode == "empty" {
					return nil
				}
				if mode == "oversized" {
					chunk := make([]byte, 64<<10)
					for i := 0; i < 129; i++ {
						_, _ = writer.Write(chunk) // A producer cannot hide writer failure.
					}
					return nil
				}
				_, e := writer.Write([]byte("profile bytes"))
				return e
			}, func(path string) (io.WriteCloser, error) {
				file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				return &heapProfileTestFile{File: file, failWrite: mode == "write", failClose: mode == "close", short: mode == "short"}, err
			}, func(string, string) error {
				renameCalls++
				return errors.New("PRIVATE_RENAME_ERROR")
			})
			if err == nil || strings.Contains(err.Error(), "PRIVATE") || artifact.bytes != 0 || mode != "rename" && renameCalls != 0 {
				t.Fatal("failed heap artifact was published or leaked a source error")
			}
			entries, readErr := os.ReadDir(directory)
			if readErr != nil || len(entries) != 0 {
				t.Fatal("failed heap profile retained an apparent complete artifact")
			}
		})
	}
}

func TestHeapProfileRealProfileIsGzipAndExclusive(t *testing.T) {
	directory := t.TempDir()
	open := func(path string) (io.WriteCloser, error) {
		return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	}
	artifact, err := writeHeapProfileArtifact(directory, "before", pprof.WriteHeapProfile, open, os.Rename)
	if err != nil || artifact.bytes <= 0 || artifact.bytes > heapProfileMaxBytes {
		t.Fatal("real bounded heap profile was not published", err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "before.heap.pb.gz"))
	if err != nil || int64(len(data)) != artifact.bytes {
		t.Fatal("published heap profile size differs")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != artifact.sha256 {
		t.Fatal("published heap profile digest differs")
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal("real heap profile has no gzip stream")
	}
	decoded, readErr := io.ReadAll(io.LimitReader(gzipReader, (64<<20)+1))
	closeErr := gzipReader.Close()
	if readErr != nil || closeErr != nil || len(decoded) == 0 || len(decoded) > 64<<20 {
		t.Fatal("real heap profile gzip stream was incomplete or oversized")
	}
	if _, err := writeHeapProfileArtifact(directory, "before", pprof.WriteHeapProfile, open, os.Rename); err == nil {
		t.Fatal("complete heap profile could be overwritten")
	}
	if err := os.WriteFile(filepath.Join(directory, "after.heap.pb.gz.partial"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeHeapProfileArtifact(directory, "after", pprof.WriteHeapProfile, open, os.Rename); err == nil {
		t.Fatal("partial heap profile could be overwritten")
	}
}

func TestHeapProfileAcknowledgementRequiresExactDigest(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, content := range []string{digest, "", digest + "\n", strings.Repeat("b", 64), strings.Repeat("a", 65)} {
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, "heap-before-copied"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		err := waitHeapProfileAck(context.Background(), directory, heapProfileMetadata{Stage: "before", SHA256: digest})
		if (err == nil) != (content == digest) {
			t.Fatal("heap profile acknowledgement did not require the exact digest")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitHeapProfileAck(ctx, t.TempDir(), heapProfileMetadata{Stage: "before", SHA256: digest}); err == nil {
		t.Fatal("cancelled profile capture waited for acknowledgement")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := waitHeapProfileAck(ctx, t.TempDir(), heapProfileMetadata{Stage: "before", SHA256: digest}); err == nil {
		t.Fatal("missing profile acknowledgement ignored the request deadline")
	}
}

func TestHeapProfileStagePublicationAndFailures(t *testing.T) {
	for _, mode := range []string{"success", "write", "emit", "ack", "rate", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			profile := &memoryProfileReport{started: time.Now()}
			writes, events, acks, rate := 0, 0, 0, 512<<10
			hooks := heapProfileCapture{directory: filepath.Join(t.TempDir(), "profiles"),
				write: func(string, string) (heapProfileArtifact, error) {
					writes++
					if mode == "write" {
						return heapProfileArtifact{}, errors.New("PRIVATE_WRITE")
					}
					return heapProfileArtifact{bytes: 10, sha256: strings.Repeat("a", 64)}, nil
				},
				emit: func(metadata heapProfileMetadata) error {
					events++
					if metadata.Version != 1 || metadata.Stage != "before" && metadata.Stage != "after" || metadata.ElapsedNanos < 0 {
						t.Fatal("invalid heap profile ready metadata")
					}
					if mode == "emit" {
						return errors.New("PRIVATE_EMIT")
					}
					return nil
				},
				ack: func(context.Context, string, heapProfileMetadata) error {
					acks++
					if mode == "ack" {
						return errors.New("copy acknowledgement unavailable")
					}
					return nil
				},
				stats: func() (int, uint32) { return rate, 1 },
			}
			if err := profile.captureHeapWith(context.Background(), "unused", "after", hooks); err == nil || writes != 0 {
				t.Fatal("heap profile skipped its before stage")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			err := profile.captureHeapWith(ctx, "unused", "before", hooks)
			if mode == "success" || mode == "rate" {
				if err != nil {
					t.Fatal(err)
				}
				if err := profile.captureHeapWith(ctx, "unused", "before", hooks); err == nil || writes != 1 {
					t.Fatal("duplicate heap profile was written")
				}
				if mode == "rate" {
					rate++
				}
				// These hooks perform no profile I/O or ACK wait. Wait for the
				// actual clock to advance on Windows instead of changing timestamps.
				for time.Since(profile.started).Nanoseconds() <= profile.HeapProfiles[0].ElapsedNanos {
					time.Sleep(time.Millisecond)
				}
				err = profile.captureHeapWith(ctx, "unused", "after", hooks)
				if mode == "success" && (err != nil || writes != 2 || events != 2 || acks != 2 || len(profile.HeapProfiles) != 2) || mode == "rate" && (err == nil || writes != 1) {
					t.Fatal("heap profile pair did not preserve stage and sampling rate", err)
				}
				return
			}
			if err == nil || strings.Contains(err.Error(), "PRIVATE") || mode == "write" && (events != 0 || acks != 0 || len(profile.HeapProfiles) != 0) || mode == "emit" && acks != 0 || mode == "ack" && len(profile.HeapProfiles) != 1 || mode == "cancel" && writes != 0 {
				t.Fatal("failed heap capture published incomplete evidence or ignored cancellation")
			}
		})
	}
}
