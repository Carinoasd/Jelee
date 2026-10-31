//go:build perf

package media

import (
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
)

// Local first-byte, seek and concurrency baseline for direct delivery (G10.1,
// G10.7, G29.5). It runs over real loopback TCP, with client and server in
// one process, so the numbers are a development-machine baseline and never an
// acceptance result. Run it with:
//
//	make bench-direct
//
// or: go test -tags perf -run TestDirectBaseline -v ./internal/adapter/media
//
// JELEE_PERF_SAMPLES (default 40) sets the samples per latency row and
// JELEE_PERF_OUT writes the report to a file as well as the test log.
const (
	perfFileSize   = 256 << 20
	perfStreamSize = 32 << 20
)

func perfSamples() int {
	var n int
	if _, err := fmt.Sscanf(os.Getenv("JELEE_PERF_SAMPLES"), "%d", &n); err != nil || n < 5 {
		return 40
	}
	return min(n, 500)
}

// perfDir creates a scratch directory below dir (a disk-backed location; the
// default temporary directory is tmpfs on many systems) and removes it after.
func perfDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(dir, "direct-baseline-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

type perfServer struct {
	server *httptest.Server
	path   string
}

func newPerfServer(t *testing.T, buffered bool) *perfServer {
	t.Helper()
	root := t.TempDir()
	if dir := os.Getenv("JELEE_PERF_DIR"); dir != "" {
		root = perfDir(t, dir)
	}
	file := filepath.Join(root, "large.mkv")
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = byte(i*7 + i>>8)
	}
	out, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	for written := 0; written < perfFileSize; written += len(chunk) {
		if _, err := out.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := out.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	source := Source{Root: root, RelativePath: "large.mkv", ContentType: "video/x-matroska"}
	handler := zeroCopyHandler(t, source, Options{})
	handler.slots = make(chan struct{}, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if buffered {
			w = bufferedOnlyWriter{w}
		}
		handler.ServeSource(w, r.WithContext(access.WithPrincipal(r.Context(), streamPrincipal("u", "s"))), "source")
	}))
	t.Cleanup(server.Close)
	return &perfServer{server: server, path: file}
}

// firstByte times a request from sending it to receiving the first body byte,
// then abandons the response by closing the connection.
func (p *perfServer) firstByte(t *testing.T, rangeHeader string) time.Duration {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, p.server.URL+"/stream", nil)
	if rangeHeader != "" {
		request.Header.Set("Range", rangeHeader)
	}
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	start := time.Now()
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var one [1]byte
	if _, err := io.ReadFull(response.Body, one[:]); err != nil {
		t.Fatal(err)
	}
	return time.Since(start)
}

type latencyRow struct {
	name        string
	cold        bool
	samples     []time.Duration
	skipped     string
	coldEffects string
}

func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := int(q*float64(len(sorted))+0.999999) - 1
	return sorted[max(0, min(len(sorted)-1, index))]
}

func (r latencyRow) format(path string) string {
	cache := "warm"
	if r.cold {
		cache = "cold"
	}
	if r.skipped != "" {
		return fmt.Sprintf("%-8s %-22s %-5s skipped: %s", path, r.name, cache, r.skipped)
	}
	sorted := append([]time.Duration(nil), r.samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	ms := func(d time.Duration) string { return fmt.Sprintf("%7.3f", float64(d.Microseconds())/1000) }
	return fmt.Sprintf("%-8s %-22s %-5s n=%-3d p50=%s p95=%s p99=%s max=%s ms", path, r.name, cache, len(sorted),
		ms(percentile(sorted, 0.50)), ms(percentile(sorted, 0.95)), ms(percentile(sorted, 0.99)), ms(sorted[len(sorted)-1]))
}

func measure(t *testing.T, p *perfServer, name string, cold bool, n int, header func(i int) string) latencyRow {
	row := latencyRow{name: name, cold: cold}
	for i := range n {
		if cold && !dropPageCache(p.path) {
			row.skipped = "page cache cannot be dropped on this platform"
			return row
		}
		row.samples = append(row.samples, p.firstByte(t, header(i)))
	}
	return row
}

func TestDirectBaseline(t *testing.T) {
	n := perfSamples()
	var report strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&report, format+"\n", args...)
	}
	line("direct play baseline (development machine, NOT an acceptance result)")
	line("date: %s  go: %s  %s/%s  cpus: %d  file: %d MiB  samples/row: %d", time.Now().UTC().Format("2006-01-02"), runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), perfFileSize>>20, n)
	line("client and server share one process over loopback TCP; first byte = request sent to first body byte received")
	line("")
	line("== first byte and seek latency (single stream)")

	rng := rand.New(rand.NewPCG(1, 2))
	const mib = 1 << 20
	maxStart := perfFileSize - 8*mib
	for _, path := range []struct {
		name     string
		buffered bool
	}{{"sendfile", false}, {"buffered", true}} {
		if !path.buffered && !zeroCopySupported {
			continue
		}
		p := newPerfServer(t, path.buffered)
		line("-- %s path: file on %s", path.name, filesystemName(filepath.Dir(p.path)))
		warmResident := residentPercent(p.path)
		dropPageCache(p.path)
		line("   page cache residency of the file: %.0f%% before drop, %.0f%% after posix_fadvise(DONTNEED)", warmResident, residentPercent(p.path))
		cursor := 0
		for _, cold := range []bool{false, true} {
			line("%s", measure(t, p, "full file", cold, n, func(int) string { return "" }).format(path.name))
			line("%s", measure(t, p, "seek random", cold, n, func(int) string {
				return fmt.Sprintf("bytes=%d-", rng.IntN(maxStart))
			}).format(path.name))
			line("%s", measure(t, p, "seek forward +16MiB", cold, n, func(int) string {
				cursor = (cursor + 16*mib) % maxStart
				return fmt.Sprintf("bytes=%d-", cursor)
			}).format(path.name))
			line("%s", measure(t, p, "seek backward -16MiB", cold, n, func(int) string {
				cursor -= 16 * mib
				if cursor < 0 {
					cursor += maxStart
				}
				return fmt.Sprintf("bytes=%d-", cursor)
			}).format(path.name))
			line("%s", measure(t, p, "sequential 1MiB ranges", cold, n, func(i int) string {
				return fmt.Sprintf("bytes=%d-%d", (i%200)*mib, (i%200+1)*mib-1)
			}).format(path.name))
		}
	}

	line("")
	line("== concurrent full downloads (%d MiB per stream, warm cache, median of 3 rounds)", perfStreamSize>>20)
	line("%-8s %7s %12s %14s %16s", "path", "streams", "total MB/s", "cpu ms/stream", "cpu ms per GiB")
	for _, path := range []struct {
		name     string
		buffered bool
	}{{"sendfile", false}, {"buffered", true}} {
		if !path.buffered && !zeroCopySupported {
			continue
		}
		p := newPerfServer(t, path.buffered)
		perfDownloads(t, p, 4) // warm-up: connections, page cache, pool buffers
		for _, streams := range []int{1, 4, 16} {
			type round struct{ mbps, cpuStream, cpuGiB float64 }
			var rounds []round
			for range 3 {
				cpuStart, cpuOK := processCPU()
				started := time.Now()
				perfDownloads(t, p, streams)
				elapsed := time.Since(started)
				total := float64(streams) * perfStreamSize
				r := round{mbps: total / 1e6 / elapsed.Seconds(), cpuStream: -1, cpuGiB: -1}
				if cpuEnd, ok := processCPU(); ok && cpuOK {
					cpu := float64((cpuEnd - cpuStart).Microseconds()) / 1000
					r.cpuStream, r.cpuGiB = cpu/float64(streams), cpu/(total/(1<<30))
				}
				rounds = append(rounds, r)
			}
			sort.Slice(rounds, func(i, j int) bool { return rounds[i].mbps < rounds[j].mbps })
			mid := rounds[len(rounds)/2]
			line("%-8s %7d %12.0f %14.1f %16.1f", path.name, streams, mid.mbps, mid.cpuStream, mid.cpuGiB)
		}
	}
	line("")
	line("cpu figures are process user+system time and include the in-process client (same on both paths).")
	line("cold rows run posix_fadvise(DONTNEED) before every request. Under WSL2 or any VM the host may still cache the virtual disk, so cold here only means 'not in the guest page cache'.")

	t.Log("\n" + report.String())
	if out := os.Getenv("JELEE_PERF_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(report.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// perfDownloads runs streams concurrent downloads of perfStreamSize bytes and
// waits for all of them.
func perfDownloads(t *testing.T, p *perfServer, streams int) {
	t.Helper()
	var wg sync.WaitGroup
	for range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request, _ := http.NewRequest(http.MethodGet, p.server.URL+"/stream", nil)
			request.Header.Set("Range", fmt.Sprintf("bytes=0-%d", perfStreamSize-1))
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Error(err)
				return
			}
			defer response.Body.Close()
			if got, err := io.Copy(io.Discard, response.Body); err != nil || got != perfStreamSize {
				t.Errorf("downloaded %d bytes: %v", got, err)
			}
		}()
	}
	wg.Wait()
}
