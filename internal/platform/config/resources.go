package config

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"strconv"
)

type ResourcesConfig struct {
	CPUFactor float64        `json:"cpuFactor"`
	IO        int            `json:"io"`
	Total     int            `json:"total"`
	Queue     int            `json:"queue"`
	Adaptive  AdaptiveConfig `json:"adaptive"`
}

// AdaptiveConfig is the optional load adaptive concurrency of G41.7. It is
// off by default; when on, the CPU, I/O and total limits above are ceilings
// the controller lowers under pressure and restores with hysteresis.
type AdaptiveConfig struct {
	Enabled         bool `json:"enabled"`
	IntervalSeconds int  `json:"intervalSeconds"`
	DwellSeconds    int  `json:"dwellSeconds"`
	CooldownSeconds int  `json:"cooldownSeconds"`
	MinPercent      int  `json:"minPercent"`
	StepPercent     int  `json:"stepPercent"`
	// Hysteresis bands: load per CPU, the throttled fraction of CFS
	// periods, PSI memory "some avg10" percent, and working set over the
	// cgroup memory limit.
	LoadHigh           float64 `json:"loadHigh"`
	LoadLow            float64 `json:"loadLow"`
	ThrottleHigh       float64 `json:"throttleHigh"`
	ThrottleLow        float64 `json:"throttleLow"`
	MemoryPressureHigh float64 `json:"memoryPressureHigh"`
	MemoryPressureLow  float64 `json:"memoryPressureLow"`
	MemoryUsageHigh    float64 `json:"memoryUsageHigh"`
	MemoryUsageLow     float64 `json:"memoryUsageLow"`
}

// DefaultAdaptiveConfig is off with the documented hysteresis bands.
func DefaultAdaptiveConfig() AdaptiveConfig {
	return AdaptiveConfig{IntervalSeconds: 10, DwellSeconds: 30, CooldownSeconds: 120, MinPercent: 25, StepPercent: 25,
		LoadHigh: 1.5, LoadLow: 0.9, ThrottleHigh: 0.3, ThrottleLow: 0.05, MemoryPressureHigh: 20, MemoryPressureLow: 5, MemoryUsageHigh: 0.92, MemoryUsageLow: 0.8}
}

func DefaultResourcesConfig() ResourcesConfig {
	return ResourcesConfig{CPUFactor: 1, IO: 16, Total: 32, Queue: 128, Adaptive: DefaultAdaptiveConfig()}
}

func band(high, low, limit float64) bool {
	return !math.IsNaN(high) && !math.IsNaN(low) && low > 0 && low < high && high <= limit
}

// Validate checks the adaptive settings even while disabled, so enabling
// them later cannot surface a latent error.
func (c AdaptiveConfig) Validate() error {
	if c.IntervalSeconds < 1 || c.IntervalSeconds > 3600 || c.DwellSeconds < 0 || c.DwellSeconds > 86400 || c.CooldownSeconds < 0 || c.CooldownSeconds > 86400 ||
		c.MinPercent < 1 || c.MinPercent > 100 || c.StepPercent < 1 || c.StepPercent > 100 {
		return errors.New("adaptive concurrency settings are outside the supported range")
	}
	if !band(c.LoadHigh, c.LoadLow, 1024) || !band(c.ThrottleHigh, c.ThrottleLow, 1) || !band(c.MemoryPressureHigh, c.MemoryPressureLow, 100) || !band(c.MemoryUsageHigh, c.MemoryUsageLow, 1) {
		return errors.New("adaptive concurrency thresholds must satisfy 0 < low < high within range")
	}
	return nil
}

// CPULimit uses the effective Go parallelism at startup (including container
// limits), rounds up fractional factors, and caps supported worker capacity.
func (c ResourcesConfig) CPULimit() int {
	return max(1, min(256, int(math.Ceil(float64(runtime.GOMAXPROCS(0))*c.CPUFactor))))
}
func (c ResourcesConfig) Validate() error {
	if math.IsNaN(c.CPUFactor) || math.IsInf(c.CPUFactor, 0) || c.CPUFactor < 0.125 || c.CPUFactor > 8 || c.IO < 1 || c.IO > 1024 || c.Total < 1 || c.Total > 1024 || c.Queue < 0 || c.Queue > 4096 {
		return errors.New("resource concurrency is outside the supported range")
	}
	return c.Adaptive.Validate()
}
func (c *ResourcesConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	if value, ok := lookup("JELEE_RESOURCE_CPU_FACTOR"); ok {
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return errors.New("invalid JELEE_RESOURCE_CPU_FACTOR")
		}
		c.CPUFactor = n
	}
	if value, ok := lookup("JELEE_RESOURCE_ADAPTIVE"); ok {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New("invalid JELEE_RESOURCE_ADAPTIVE")
		}
		c.Adaptive.Enabled = b
	}
	for key, target := range map[string]*int{"JELEE_RESOURCE_IO": &c.IO, "JELEE_RESOURCE_TOTAL": &c.Total, "JELEE_RESOURCE_QUEUE": &c.Queue,
		"JELEE_RESOURCE_ADAPTIVE_INTERVAL_SECONDS": &c.Adaptive.IntervalSeconds, "JELEE_RESOURCE_ADAPTIVE_DWELL_SECONDS": &c.Adaptive.DwellSeconds,
		"JELEE_RESOURCE_ADAPTIVE_COOLDOWN_SECONDS": &c.Adaptive.CooldownSeconds, "JELEE_RESOURCE_ADAPTIVE_MIN_PERCENT": &c.Adaptive.MinPercent} {
		if value, ok := lookup(key); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", key)
			}
			*target = n
		}
	}
	return nil
}
