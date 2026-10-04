package runtime

import (
	"os"
	"path/filepath"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/diag"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/telemetry"
)

// registerOpsMetrics adds the families the default alert rules read
// (G50.6): shared operational gauges, the memory limit and the filesystems
// of the writable directories doctor also checks.
func registerOpsMetrics(c config.Config, source app.OpsMetricsSource, metrics *telemetry.Metrics) error {
	if err := metrics.RegisterOps(source); err != nil {
		return err
	}
	if err := metrics.RegisterMemoryLimit(); err != nil {
		return err
	}
	return metrics.RegisterStorage(storageVolumes(c), readStorage)
}

// storageVolumes names directories by the configuration key doctor uses;
// paths never become labels.
func storageVolumes(c config.Config) []telemetry.StorageVolume {
	volumes := []telemetry.StorageVolume{{Name: "tempdir", Path: os.TempDir()}}
	if c.Images.TempRoot != "" {
		volumes = append(volumes, telemetry.StorageVolume{Name: "images.tempRoot", Path: c.Images.TempRoot})
	}
	if c.Images.StoreRoot != "" {
		volumes = append(volumes, telemetry.StorageVolume{Name: "images.storeRoot", Path: c.Images.StoreRoot})
	}
	if (c.Logging.Output == "file" || c.Logging.Output == "both") && c.Logging.File.Path != "" {
		volumes = append(volumes, telemetry.StorageVolume{Name: "logging.file", Path: filepath.Dir(c.Logging.File.Path)})
	}
	return volumes
}

func readStorage(path string) (telemetry.StorageUsage, error) {
	usage, err := diag.ReadDisk(path)
	return telemetry.StorageUsage{TotalBytes: usage.TotalBytes, AvailableBytes: usage.FreeBytes}, err
}
