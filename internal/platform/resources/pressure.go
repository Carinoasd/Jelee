package resources

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
)

// pressureFileLimit bounds every procfs or cgroupfs read.
const pressureFileLimit = 8 << 10

// NewSystemSource returns the pressure source of this platform. Linux reads
// /proc/loadavg, the cgroup v2 cpu.max, cpu.stat, memory.pressure,
// memory.current, memory.max and memory.stat of this process, and falls
// back to the host PSI file. Other platforms (including Windows, which has
// no load average, cgroup or PSI equivalent readable without new
// dependencies) report ErrPressureUnsupported and adaptive concurrency stays
// off there.
func NewSystemSource() PressureSource {
	if goruntime.GOOS != "linux" {
		return unsupportedSource{}
	}
	return NewFSSource("/", goruntime.NumCPU())
}

type unsupportedSource struct{}

func (unsupportedSource) Read(context.Context) (Reading, error) {
	return Reading{}, ErrPressureUnsupported
}

// FSSource reads Linux pressure files below root, so tests can point it at a
// fixture tree. It keeps the previous cpu.stat counters to report the
// throttled fraction between two readings.
type FSSource struct {
	root   string
	numCPU int

	mu           sync.Mutex
	prevPeriods  uint64
	prevThrottle uint64
	havePrev     bool
}

// NewFSSource reads below root and compares load with numCPU unless a
// cgroup quota is smaller.
func NewFSSource(root string, numCPU int) *FSSource {
	return &FSSource{root: root, numCPU: max(1, numCPU)}
}

func (s *FSSource) file(name string) ([]byte, error) {
	f, err := os.Open(filepath.Join(s.root, filepath.FromSlash(name)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, pressureFileLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > pressureFileLimit {
		return nil, errors.New("pressure file too large")
	}
	return data, nil
}

// cgroupDir returns the cgroup v2 directory of this process relative to
// root, or "" when the process is not in a unified hierarchy.
func (s *FSSource) cgroupDir() string {
	data, err := s.file("proc/self/cgroup")
	if err != nil {
		return ""
	}
	return parseCgroupPath(data)
}

// parseCgroupPath extracts the unified ("0::") entry of /proc/self/cgroup.
func parseCgroupPath(data []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		rest, ok := strings.CutPrefix(scanner.Text(), "0::")
		if !ok {
			continue
		}
		clean := path.Clean("/" + rest)
		if strings.Contains(rest, "..") || strings.ContainsRune(rest, 0) {
			return ""
		}
		return path.Join("sys/fs/cgroup", clean)
	}
	return ""
}

// Read never fails because one signal is missing; it fails only when the
// context ended or no signal at all could be read.
func (s *FSSource) Read(ctx context.Context) (Reading, error) {
	var r Reading
	if err := ctx.Err(); err != nil {
		return r, err
	}
	cpus := float64(s.numCPU)
	dir := s.cgroupDir()
	if dir != "" {
		if data, err := s.file(dir + "/cpu.max"); err == nil {
			if quota, ok := parseCPUMax(data); ok && quota < cpus {
				cpus = quota
			}
		}
		if data, err := s.file(dir + "/cpu.stat"); err == nil {
			if periods, throttled, ok := parseCPUStat(data); ok {
				s.mu.Lock()
				if s.havePrev && periods > s.prevPeriods && throttled >= s.prevThrottle {
					r.Throttled = float64(throttled-s.prevThrottle) / float64(periods-s.prevPeriods)
					r.HasThrottle = true
				}
				s.prevPeriods, s.prevThrottle, s.havePrev = periods, throttled, true
				s.mu.Unlock()
			}
		}
		if data, err := s.file(dir + "/memory.pressure"); err == nil {
			r.MemoryPressure, r.HasMemoryPressure = parsePSISome(data)
		}
		if current, err := s.file(dir + "/memory.current"); err == nil {
			if limit, err := s.file(dir + "/memory.max"); err == nil {
				stat, _ := s.file(dir + "/memory.stat")
				r.MemoryUsage, r.HasMemoryUsage = memoryUsage(current, limit, stat)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Reading{}, err
	}
	if !r.HasMemoryPressure {
		if data, err := s.file("proc/pressure/memory"); err == nil {
			r.MemoryPressure, r.HasMemoryPressure = parsePSISome(data)
		}
	}
	if data, err := s.file("proc/loadavg"); err == nil {
		if fields := strings.Fields(string(data)); len(fields) > 0 {
			if load, err := strconv.ParseFloat(fields[0], 64); err == nil && load >= 0 {
				r.Load1, r.CPUs, r.HasLoad = load, cpus, true
			}
		}
	}
	if !r.HasLoad && !r.HasThrottle && !r.HasMemoryPressure && !r.HasMemoryUsage {
		return Reading{}, errors.New("no system pressure signal is readable")
	}
	return r, nil
}

// parseCPUMax reads "quota period" or "max period" and returns the CPU
// capacity, false when unlimited or malformed.
func parseCPUMax(data []byte) (float64, bool) {
	fields := strings.Fields(string(data))
	if len(fields) != 2 || fields[0] == "max" {
		return 0, false
	}
	quota, err1 := strconv.ParseUint(fields[0], 10, 64)
	period, err2 := strconv.ParseUint(fields[1], 10, 64)
	if err1 != nil || err2 != nil || quota == 0 || period == 0 {
		return 0, false
	}
	return float64(quota) / float64(period), true
}

func parseCPUStat(data []byte) (periods, throttled uint64, ok bool) {
	var seenPeriods, seenThrottled bool
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "nr_periods":
			periods, seenPeriods = n, true
		case "nr_throttled":
			throttled, seenThrottled = n, true
		}
	}
	return periods, throttled, seenPeriods && seenThrottled
}

// parsePSISome returns avg10 of the "some" line of a PSI file.
func parsePSISome(data []byte) (float64, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "some" {
			continue
		}
		for _, field := range fields[1:] {
			if value, ok := strings.CutPrefix(field, "avg10="); ok {
				v, err := strconv.ParseFloat(value, 64)
				if err != nil || v < 0 || v > 100 {
					return 0, false
				}
				return v, true
			}
		}
	}
	return 0, false
}

// memoryUsage is (memory.current - inactive_file) / memory.max, the working
// set measure container runtimes use. "max" (no limit) yields no signal.
func memoryUsage(current, limit, stat []byte) (float64, bool) {
	l := strings.TrimSpace(string(limit))
	if l == "max" {
		return 0, false
	}
	ceiling, err := strconv.ParseUint(l, 10, 64)
	if err != nil || ceiling == 0 {
		return 0, false
	}
	used, err := strconv.ParseUint(strings.TrimSpace(string(current)), 10, 64)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(stat), "\n") {
		if value, ok := strings.CutPrefix(line, "inactive_file "); ok {
			if inactive, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64); err == nil && inactive <= used {
				used -= inactive
			}
			break
		}
	}
	return float64(used) / float64(ceiling), true
}
