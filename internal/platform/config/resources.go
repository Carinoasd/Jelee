package config

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"strconv"
)

type ResourcesConfig struct {
	CPUFactor float64 `json:"cpuFactor"`
	IO        int     `json:"io"`
	Total     int     `json:"total"`
	Queue     int     `json:"queue"`
}

func DefaultResourcesConfig() ResourcesConfig {
	return ResourcesConfig{CPUFactor: 1, IO: 16, Total: 32, Queue: 128}
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
	return nil
}
func (c *ResourcesConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	if value, ok := lookup("JELEE_RESOURCE_CPU_FACTOR"); ok {
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return errors.New("invalid JELEE_RESOURCE_CPU_FACTOR")
		}
		c.CPUFactor = n
	}
	for key, target := range map[string]*int{"JELEE_RESOURCE_IO": &c.IO, "JELEE_RESOURCE_TOTAL": &c.Total, "JELEE_RESOURCE_QUEUE": &c.Queue} {
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
