//go:build jelee_probe_tests

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

const memoryCgroupRoot = "/sys/fs/cgroup"

type memoryRuntimeSettings struct {
	GOGCPercent        uint64 `json:"gogcPercent"`
	GoMemoryLimitBytes uint64 `json:"goMemoryLimitBytes"`
}

type memoryRuntimeCounters struct {
	TotalAllocBytes uint64 `json:"totalAllocBytes"`
	NumGC           uint32 `json:"numGC"`
	PauseTotalNS    uint64 `json:"pauseTotalNs"`
}

type memoryCgroupSnapshot struct {
	CgroupVersion int               `json:"cgroupVersion"`
	CurrentBytes  uint64            `json:"currentBytes"`
	PeakBytes     uint64            `json:"peakBytes"`
	MaxBytes      uint64            `json:"maxBytes"`
	SwapMaxBytes  uint64            `json:"swapMaxBytes"`
	Events        map[string]uint64 `json:"events"`
}

type memoryKDFReport struct {
	MemoryKiB             uint32 `json:"memoryKiB"`
	Iterations            uint32 `json:"iterations"`
	Parallelism           uint8  `json:"parallelism"`
	Concurrency           int    `json:"concurrency"`
	Completed             int    `json:"completed"`
	PeakAdmitted          int    `json:"peakAdmitted"`
	WorkerActiveDuringKDF bool   `json:"workerActiveDuringKDF"`
	HashCompleted         int    `json:"hashCompleted"`
	VerifyCompleted       int    `json:"verifyCompleted"`
}

type memoryProfileReport struct {
	Version         int                   `json:"version"`
	Runtime         memoryRuntimeSettings `json:"runtime"`
	Before          memoryCgroupSnapshot  `json:"before"`
	After           memoryCgroupSnapshot  `json:"after"`
	RuntimeBefore   memoryRuntimeCounters `json:"runtimeBefore"`
	RuntimeAfter    memoryRuntimeCounters `json:"runtimeAfter"`
	KDF             memoryKDFReport       `json:"kdf"`
	Resident        residentProfile       `json:"resident"`
	ElapsedMillis   int64                 `json:"elapsedMillis"`
	started         time.Time
	residentSampler *residentSampler
}

func readMemoryRuntimeSettings() (memoryRuntimeSettings, error) {
	samples := []metrics.Sample{{Name: "/gc/gogc:percent"}, {Name: "/gc/gomemlimit:bytes"}}
	metrics.Read(samples)
	for _, sample := range samples {
		if sample.Value.Kind() != metrics.KindUint64 {
			return memoryRuntimeSettings{}, errors.New("required runtime memory metric unavailable")
		}
	}
	return memoryRuntimeSettings{GOGCPercent: samples[0].Value.Uint64(), GoMemoryLimitBytes: samples[1].Value.Uint64()}, nil
}

func readMemoryRuntimeCounters() memoryRuntimeCounters {
	var stats goruntime.MemStats
	goruntime.ReadMemStats(&stats)
	return memoryRuntimeCounters{TotalAllocBytes: stats.TotalAlloc, NumGC: stats.NumGC, PauseTotalNS: stats.PauseTotalNs}
}

func readMemoryCgroup(root string) (memoryCgroupSnapshot, error) {
	read := func(name string) (string, error) {
		file, err := os.Open(filepath.Join(root, name))
		if err != nil {
			return "", fmt.Errorf("required cgroup file %s unavailable", name)
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, 4097))
		if err != nil || len(data) > 4096 {
			return "", fmt.Errorf("required cgroup file %s unreadable or oversized", name)
		}
		return strings.TrimSpace(string(data)), nil
	}
	if _, err := read("cgroup.controllers"); err != nil {
		return memoryCgroupSnapshot{}, err
	}
	result := memoryCgroupSnapshot{CgroupVersion: 2, Events: make(map[string]uint64)}
	for _, field := range []struct {
		name string
		out  *uint64
	}{{"memory.current", &result.CurrentBytes}, {"memory.peak", &result.PeakBytes}, {"memory.max", &result.MaxBytes}, {"memory.swap.max", &result.SwapMaxBytes}} {
		text, err := read(field.name)
		if err != nil {
			return memoryCgroupSnapshot{}, err
		}
		value, err := strictMemoryUint(text)
		if err != nil {
			return memoryCgroupSnapshot{}, fmt.Errorf("cgroup file %s must be a finite byte count", field.name)
		}
		*field.out = value
	}
	if result.MaxBytes == 0 || result.PeakBytes < result.CurrentBytes {
		return memoryCgroupSnapshot{}, errors.New("invalid cgroup memory limits or peak")
	}
	events, err := read("memory.events")
	if err != nil {
		return memoryCgroupSnapshot{}, err
	}
	for _, line := range strings.Split(events, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return memoryCgroupSnapshot{}, errors.New("malformed cgroup memory events")
		}
		value, err := strictMemoryUint(fields[1])
		if _, duplicate := result.Events[fields[0]]; err != nil || duplicate {
			return memoryCgroupSnapshot{}, errors.New("invalid or duplicate cgroup memory event")
		}
		result.Events[fields[0]] = value
	}
	for _, name := range []string{"low", "high", "max", "oom", "oom_kill"} {
		if _, ok := result.Events[name]; !ok {
			return memoryCgroupSnapshot{}, fmt.Errorf("required cgroup event %s missing", name)
		}
	}
	return result, nil
}

func strictMemoryUint(text string) (uint64, error) {
	if text == "" {
		return 0, errors.New("empty unsigned integer")
	}
	for _, digit := range text {
		if digit < '0' || digit > '9' {
			return 0, errors.New("invalid unsigned integer")
		}
	}
	return strconv.ParseUint(text, 10, 64)
}

func startMemoryProfile() (*memoryProfileReport, error) {
	runtime, err := readMemoryRuntimeSettings()
	if err != nil {
		return nil, err
	}
	before, err := readMemoryCgroup(memoryCgroupRoot)
	if err != nil {
		return nil, err
	}
	profile := &memoryProfileReport{Version: 1, Runtime: runtime, Before: before, RuntimeBefore: readMemoryRuntimeCounters(), started: time.Now()}
	profile.residentSampler, err = startResidentSampler(profile.started)
	if err != nil {
		return nil, err
	}
	return profile, nil
}

func (profile *memoryProfileReport) finish(t *testing.T) {
	t.Helper()
	resident, err := profile.residentSampler.finish()
	profile.Resident = resident
	if err != nil {
		t.Error("resident memory sampling failed", err)
	}
	after, err := readMemoryCgroup(memoryCgroupRoot)
	if err != nil {
		t.Error("read final memory profile cgroup evidence", err)
		return
	}
	runtime, err := readMemoryRuntimeSettings()
	if err != nil || runtime != profile.Runtime {
		t.Error("effective runtime memory settings changed during acceptance")
		return
	}
	profile.After, profile.RuntimeAfter, profile.ElapsedMillis = after, readMemoryRuntimeCounters(), time.Since(profile.started).Milliseconds()
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"memoryProfile": profile}); err != nil {
		t.Error("write memory profile evidence")
	}
}

// A real completed NFO file read remains in flight while the KDF pair runs.
// This records overlap with live worker state, not a stalled operating-system read.
type memoryWorkerBarrier struct {
	entered, release chan struct{}
	enterOnce        sync.Once
	releaseOnce      sync.Once
}

func newMemoryWorkerBarrier() *memoryWorkerBarrier {
	return &memoryWorkerBarrier{entered: make(chan struct{}), release: make(chan struct{})}
}

func (barrier *memoryWorkerBarrier) wait(ctx context.Context) error {
	barrier.enterOnce.Do(func() { close(barrier.entered) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-barrier.release:
		return ctx.Err()
	}
}

func (barrier *memoryWorkerBarrier) close() {
	barrier.releaseOnce.Do(func() { close(barrier.release) })
}

// This acceptance context depends on password.Hasher's documented-in-code
// sequence: usable.Err, deriveBounded entry Err, then Err after taking a slot.
// The third check holds a real hasher slot. Two arrivals therefore prove both
// slots are occupied before releasing either call into the real Argon2id.
// No derive function, entropy source, password result, or hasher is substituted.
type memoryKDFContext struct {
	context.Context
	checks  atomic.Int32
	entered chan<- struct{}
	release <-chan struct{}
}

func (ctx *memoryKDFContext) Err() error {
	if err := ctx.Context.Err(); err != nil {
		return err
	}
	if ctx.checks.Add(1) == 3 {
		select {
		case ctx.entered <- struct{}{}:
		case <-ctx.Context.Done():
			return ctx.Context.Err()
		}
		select {
		case <-ctx.release:
		case <-ctx.Context.Done():
		}
	}
	return ctx.Context.Err()
}

func exerciseMemoryKDF(parent context.Context, hasher *password.Hasher, secret, encoded string, worker *memoryWorkerBarrier, active func() bool, report *memoryKDFReport) error {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	select {
	case <-worker.entered:
	case <-ctx.Done():
		return errors.New("memory profile worker read did not enter")
	}
	if !active() {
		return errors.New("memory profile worker read was not active")
	}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	type result struct {
		hash bool
		err  error
	}
	done := make(chan result, 2)
	for _, hash := range []bool{true, false} {
		go func() {
			callCtx := &memoryKDFContext{Context: ctx, entered: entered, release: release}
			var err error
			if hash {
				var generated string
				generated, err = hasher.Hash(callCtx, secret)
				if err == nil && !strings.HasPrefix(generated, "$argon2id$v=19$m=65536,t=3,p=2$") {
					err = errors.New("memory profile hash did not use production costs")
				}
			} else {
				var matched bool
				matched, err = hasher.Verify(callCtx, secret, encoded)
				if err == nil && !matched {
					err = errors.New("memory profile password verification failed")
				}
			}
			done <- result{hash: hash, err: err}
		}()
	}
	var admissionErr error
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			admissionErr = errors.New("both real password slots were not admitted")
		}
	}
	if admissionErr == nil && !active() {
		admissionErr = errors.New("worker exited before real KDF admission")
	}
	close(release)
	if admissionErr != nil {
		cancel()
	}
	// Argon2id is synchronous and does not have a context API. Join both real
	// calls even after cancellation; never detach expensive KDF work.
	for range 2 {
		result := <-done
		if result.err != nil {
			admissionErr = errors.Join(admissionErr, result.err)
			continue
		}
		report.Completed++
		if result.hash {
			report.HashCompleted++
		} else {
			report.VerifyCompleted++
		}
	}
	if admissionErr != nil {
		return admissionErr
	}
	if !active() {
		return errors.New("worker exited before both KDF operations completed")
	}
	report.PeakAdmitted, report.WorkerActiveDuringKDF = 2, true
	return nil
}

func TestMemoryProfileRuntimeSubprocess(t *testing.T) {
	if os.Getenv("JELEE_MEMORY_RUNTIME_CHILD") == "true" {
		settings, err := readMemoryRuntimeSettings()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"runtime": settings}); err != nil {
			t.Fatal("write child runtime settings")
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("find current test executable")
	}
	for _, tc := range []struct {
		name, gogc, limit string
		want              memoryRuntimeSettings
	}{
		{"native-defaults", "", "", memoryRuntimeSettings{100, math.MaxInt64}},
		{"profile-defaults", "100", "512MiB", memoryRuntimeSettings{100, 512 << 20}},
		{"operator-override", "50", "384MiB", memoryRuntimeSettings{50, 384 << 20}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestMemoryProfileRuntimeSubprocess$")
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if !strings.EqualFold(key, "GOGC") && !strings.EqualFold(key, "GOMEMLIMIT") && !strings.EqualFold(key, "JELEE_MEMORY_RUNTIME_CHILD") {
					command.Env = append(command.Env, entry)
				}
			}
			command.Env = append(command.Env, "JELEE_MEMORY_RUNTIME_CHILD=true")
			if tc.gogc != "" {
				command.Env = append(command.Env, "GOGC="+tc.gogc, "GOMEMLIMIT="+tc.limit)
			}
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatal("runtime startup subprocess failed")
			}
			found := false
			for _, line := range strings.Split(string(output), "\n") {
				if !strings.HasPrefix(line, `{"runtime":`) {
					continue
				}
				var record struct {
					Runtime memoryRuntimeSettings `json:"runtime"`
				}
				if found || json.Unmarshal([]byte(line), &record) != nil || record.Runtime != tc.want {
					t.Fatal("startup environment did not set the effective runtime values")
				}
				found = true
			}
			if !found {
				t.Fatal("startup runtime metrics report missing")
			}
		})
	}
}

func TestMemoryProfileCgroupEvidenceRequired(t *testing.T) {
	base := map[string]string{"cgroup.controllers": "cpu memory pids\n", "memory.current": "1024\n", "memory.peak": "2048\n", "memory.max": "805306368\n", "memory.swap.max": "0\n", "memory.events": "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n"}
	for _, tc := range []struct{ name, file, value string }{
		{name: "valid-without-optional-group-kill"},
		{name: "missing-file", file: "memory.peak"},
		{name: "unbounded-memory", file: "memory.max", value: "max\n"},
		{name: "unbounded-swap", file: "memory.swap.max", value: "max\n"},
		{name: "negative-value", file: "memory.current", value: "-1\n"},
		{name: "missing-event", file: "memory.events", value: "low 0\nhigh 0\nmax 0\noom 0\n"},
		{name: "duplicate-event", file: "memory.events", value: base["memory.events"] + "oom 1\n"},
		{name: "oversized-event-file", file: "memory.events", value: strings.Repeat("x", 4097)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for file, value := range base {
				if file == tc.file {
					if tc.value == "" {
						continue
					}
					value = tc.value
				}
				if err := os.WriteFile(filepath.Join(root, file), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readMemoryCgroup(root)
			if tc.file != "" {
				if err == nil {
					t.Fatal("invalid or missing cgroup evidence accepted")
				}
				return
			}
			if err != nil || got.CgroupVersion != 2 || got.CurrentBytes != 1024 || got.PeakBytes != 2048 || got.MaxBytes != 768<<20 || got.SwapMaxBytes != 0 || len(got.Events) != 5 {
				t.Fatal("real cgroup fields were not retained", err)
			}
			if _, invented := got.Events["oom_group_kill"]; invented {
				t.Fatal("missing optional cgroup event was replaced with a false zero")
			}
		})
	}
}

func TestMemoryProfileSnapshot(t *testing.T) {
	if os.Getenv("JELEE_MEMORY_SNAPSHOT") != "true" {
		t.Skip("memory snapshot requires an owned production-profile container")
	}
	if os.Getuid() != 65532 {
		t.Fatal("memory snapshot requires nonroot production UID")
	}
	runtime, err := readMemoryRuntimeSettings()
	if err != nil {
		t.Fatal(err)
	}
	cgroup, err := readMemoryCgroup(memoryCgroupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"memoryProfileSnapshot": map[string]any{"runtime": runtime, "cgroup": cgroup}}); err != nil {
		t.Fatal("write memory snapshot")
	}
}

func TestMemoryProfileOOMProbe(t *testing.T) {
	if os.Getenv("JELEE_MEMORY_OOM_PROBE") != "true" {
		t.Skip("OOM probe requires a separate owned constrained container")
	}
	if os.Getuid() != 65532 {
		t.Fatal("OOM probe requires nonroot production UID")
	}
	cgroup, err := readMemoryCgroup(memoryCgroupRoot)
	if err != nil || cgroup.MaxBytes != 64<<20 || cgroup.SwapMaxBytes != 0 {
		t.Fatal("OOM probe requires cgroup v2 hard 64 MiB and no swap")
	}
	runtime, err := readMemoryRuntimeSettings()
	if err != nil || runtime.GOGCPercent != math.MaxUint64 || runtime.GoMemoryLimitBytes != math.MaxInt64 {
		t.Fatal("OOM probe requires effective GOGC=off and GOMEMLIMIT=off")
	}
	fmt.Println(`{"memoryOOMProbe":"armed","hardLimitBytes":67108864,"attemptBytes":134217728}`)
	retained := make([][]byte, 0, 128)
	for range 128 {
		allocation := make([]byte, 1<<20)
		for page := 0; page < len(allocation); page += 4096 {
			allocation[page] = 1
		}
		retained = append(retained, allocation)
	}
	goruntime.KeepAlive(retained)
	t.Fatal("controlled allocation survived the required cgroup hard limit")
}
