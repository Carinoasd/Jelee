package config

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
)

// LoggingConfig implements G46.1/G46.2/G46.5 settings. Empty or zero fields
// select defaults so partially built configurations stay valid.
type LoggingConfig struct {
	// Level is debug, info, warn or error. DEBUG is never the default.
	Level string `json:"level"`
	// Components maps a G46.2 scope (or alias such as "ignore") to a level.
	Components map[string]string `json:"components"`
	// Format is json or console and applies to stdout only.
	Format string `json:"format"`
	// Output is stdout (container mode), file or both.
	Output string        `json:"output"`
	File   LogFileConfig `json:"file"`
	// BufferEntries bounds each asynchronous sink queue.
	BufferEntries int `json:"bufferEntries"`
	// IPMode is redact or mask; PathMode is redact or relative.
	IPMode    string   `json:"ipMode"`
	PathMode  string   `json:"pathMode"`
	PathRoots []string `json:"pathRoots"`
	// TraceSampleRate is the share of traces whose span records are kept
	// (G46.6). Ordinary log records are never sampled, and a security event
	// forces its trace to be kept.
	TraceSampleRate float64 `json:"traceSampleRate"`
}

type LogFileConfig struct {
	Path        string `json:"path"`
	MaxSizeMB   int    `json:"maxSizeMB"`
	RotateHours int    `json:"rotateHours"`
	MaxBackups  int    `json:"maxBackups"`
	Compress    bool   `json:"compress"`
}

func DefaultLoggingConfig() LoggingConfig {
	return LoggingConfig{Level: "info", Format: "json", Output: "stdout", BufferEntries: logging.DefaultBufferEntries, IPMode: "redact", PathMode: "redact", TraceSampleRate: tracing.DefaultSampleRate, File: LogFileConfig{MaxSizeMB: 100, RotateHours: 24, MaxBackups: 7, Compress: true}}
}

// Validate never includes configured values in its errors.
func (c LoggingConfig) Validate() error {
	if c.Level != "" {
		if _, err := logging.ParseLevel(c.Level); err != nil {
			return errors.New("invalid logging level")
		}
	}
	if len(c.Components) > len(logging.Components)+2 {
		return errors.New("too many logging component levels")
	}
	for component, level := range c.Components {
		if _, ok := logging.ScopeFor(component); !ok {
			return errors.New("unknown logging component")
		}
		if _, err := logging.ParseLevel(level); err != nil {
			return errors.New("invalid logging component level")
		}
	}
	switch c.Format {
	case "", "json", "console":
	default:
		return errors.New("logging format must be json or console")
	}
	switch c.Output {
	case "", "stdout":
	case "file", "both":
		if c.File.Path == "" || !filepath.IsAbs(c.File.Path) || strings.ContainsAny(c.File.Path, "\x00\r\n") {
			return errors.New("logging file output requires an absolute file path")
		}
	default:
		return errors.New("logging output must be stdout, file or both")
	}
	if c.File.MaxSizeMB < 0 || c.File.MaxSizeMB > 10240 || c.File.RotateHours < 0 || c.File.RotateHours > 24*31 || c.File.MaxBackups < 0 || c.File.MaxBackups > 1000 {
		return errors.New("logging rotation limits are outside supported ranges")
	}
	if c.BufferEntries < 0 || c.BufferEntries > 1<<20 {
		return errors.New("logging buffer size is outside supported range")
	}
	switch c.IPMode {
	case "", "redact", "mask":
	default:
		return errors.New("logging IP mode must be redact or mask")
	}
	switch c.PathMode {
	case "", "redact", "relative":
	default:
		return errors.New("logging path mode must be redact or relative")
	}
	if !tracing.ValidSampleRate(c.TraceSampleRate) {
		return errors.New("trace sample rate must be between 0 and 1")
	}
	if len(c.PathRoots) > 64 {
		return errors.New("too many logging path roots")
	}
	for _, root := range c.PathRoots {
		if root == "" || !filepath.IsAbs(root) || strings.ContainsAny(root, "\x00\r\n") {
			return errors.New("logging path roots must be absolute")
		}
	}
	return nil
}

// Options converts a validated configuration into logging options.
func (c LoggingConfig) Options() logging.Options {
	opts := logging.Options{
		Level:         slog.LevelInfo,
		Format:        logging.Format(c.Format),
		Output:        logging.Output(c.Output),
		BufferEntries: c.BufferEntries,
		IPMode:        logging.IPMode(c.IPMode),
		PathMode:      logging.PathMode(c.PathMode),
		PathRoots:     append([]string(nil), c.PathRoots...),
		File: logging.RotateOptions{
			Path:       c.File.Path,
			MaxBytes:   int64(c.File.MaxSizeMB) << 20,
			Interval:   time.Duration(c.File.RotateHours) * time.Hour,
			MaxBackups: c.File.MaxBackups,
			Compress:   c.File.Compress,
		},
	}
	if level, err := logging.ParseLevel(c.Level); err == nil {
		opts.Level = level
	}
	if len(c.Components) > 0 {
		opts.Components = make(map[string]slog.Level, len(c.Components))
		for component, name := range c.Components {
			if level, err := logging.ParseLevel(name); err == nil {
				opts.Components[component] = level
			}
		}
	}
	return opts
}

func (c *LoggingConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	for name, target := range map[string]*string{"JELEE_LOG_LEVEL": &c.Level, "JELEE_LOG_FORMAT": &c.Format, "JELEE_LOG_OUTPUT": &c.Output, "JELEE_LOG_FILE": &c.File.Path, "JELEE_LOG_IP_MODE": &c.IPMode, "JELEE_LOG_PATH_MODE": &c.PathMode} {
		if value, ok := lookup(name); ok {
			*target = value
		}
	}
	for name, target := range map[string]*int{"JELEE_LOG_MAX_SIZE_MB": &c.File.MaxSizeMB, "JELEE_LOG_ROTATE_HOURS": &c.File.RotateHours, "JELEE_LOG_MAX_BACKUPS": &c.File.MaxBackups, "JELEE_LOG_BUFFER_ENTRIES": &c.BufferEntries} {
		if value, ok := lookup(name); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", name)
			}
			*target = n
		}
	}
	if value, ok := lookup("JELEE_TRACE_SAMPLE_RATE"); ok {
		rate, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return errors.New("invalid JELEE_TRACE_SAMPLE_RATE")
		}
		c.TraceSampleRate = rate
	}
	if value, ok := lookup("JELEE_LOG_COMPRESS"); ok {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New("invalid JELEE_LOG_COMPRESS")
		}
		c.File.Compress = b
	}
	if value, ok := lookup("JELEE_LOG_PATH_ROOTS"); ok {
		c.PathRoots = nil
		if strings.TrimSpace(value) != "" {
			c.PathRoots = filepath.SplitList(value)
		}
	}
	// JELEE_LOG_COMPONENTS is "scan=debug,http=warn"; it replaces file values.
	if value, ok := lookup("JELEE_LOG_COMPONENTS"); ok {
		c.Components = map[string]string{}
		for _, pair := range strings.Split(value, ",") {
			if strings.TrimSpace(pair) == "" {
				continue
			}
			component, level, found := strings.Cut(pair, "=")
			if !found {
				return errors.New("invalid JELEE_LOG_COMPONENTS")
			}
			c.Components[strings.TrimSpace(component)] = strings.TrimSpace(level)
		}
	}
	return nil
}
