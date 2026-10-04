package config

import (
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SubtitleOCRConfig enables recognizing the text of PGS and VobSub tracks
// in Matroska sources as additional SRT tracks (G15.6). It is off by
// default and needs Matroska extraction, a private cache root of its own
// and the optional Tesseract runtime; without the runtime nothing is
// queued and doctor reports the tool.
type SubtitleOCRConfig struct {
	Enable        bool   `json:"enable"`
	CacheRoot     string `json:"cacheRoot"`
	CacheMaxBytes int64  `json:"cacheMaxBytes"`
	// Languages are Tesseract language codes among the pinned ones (eng,
	// chi_tra, chi_sim, jpn), in priority order.
	Languages []string `json:"languages"`
	// PicturesPerMinute caps recognized pictures in any rolling minute.
	PicturesPerMinute int `json:"picturesPerMinute"`
	// Concurrency is the number of simultaneous Tesseract processes.
	Concurrency int `json:"concurrency"`
	// QueueSize bounds sources waiting for OCR; further requests are dropped
	// and asked again by the next lookup.
	QueueSize int `json:"queueSize"`
}

// OCR bounds. The language list mirrors the manifest's pinned data files
// (tools.OCRLanguages); the runtime re-checks it against the manifest.
var ocrLanguages = []string{"eng", "chi_tra", "chi_sim", "jpn"}

const (
	maxOCRConcurrency = 4
	maxOCRQueue       = 256
	maxOCRPictures    = 6000
)

// DefaultSubtitleOCRConfig keeps OCR off: English, 60 pictures a minute,
// one process, a queue of 16 sources and a 256 MiB cache.
func DefaultSubtitleOCRConfig() SubtitleOCRConfig {
	return SubtitleOCRConfig{CacheMaxBytes: 256 << 20, Languages: []string{"eng"}, PicturesPerMinute: 60, Concurrency: 1, QueueSize: 16}
}

// Validate checks the OCR settings when OCR is enabled.
func (c SubtitleOCRConfig) Validate() error {
	if !c.Enable {
		return nil
	}
	if len(c.CacheRoot) > 4096 || !utf8.ValidString(c.CacheRoot) || strings.ContainsFunc(c.CacheRoot, unicode.IsControl) || !filepath.IsAbs(c.CacheRoot) || filepath.Clean(c.CacheRoot) != c.CacheRoot {
		return errors.New("subtitle OCR cacheRoot must be an absolute private directory")
	}
	if c.CacheMaxBytes < 16<<20 || c.CacheMaxBytes > 1<<40 {
		return errors.New("subtitle OCR cacheMaxBytes is outside supported bounds")
	}
	if len(c.Languages) < 1 || len(c.Languages) > len(ocrLanguages) {
		return errors.New("subtitle OCR languages must list 1 to 4 of eng, chi_tra, chi_sim, jpn")
	}
	for index, language := range c.Languages {
		if !slices.Contains(ocrLanguages, language) || slices.Contains(c.Languages[:index], language) {
			return errors.New("subtitle OCR languages must list 1 to 4 of eng, chi_tra, chi_sim, jpn")
		}
	}
	if c.PicturesPerMinute < 1 || c.PicturesPerMinute > maxOCRPictures {
		return errors.New("subtitle OCR picturesPerMinute must be between 1 and 6000")
	}
	if c.Concurrency < 1 || c.Concurrency > maxOCRConcurrency {
		return errors.New("subtitle OCR concurrency must be between 1 and 4")
	}
	if c.QueueSize < 1 || c.QueueSize > maxOCRQueue {
		return errors.New("subtitle OCR queueSize must be between 1 and 256")
	}
	return nil
}

// within reports whether path equals root or lies below it.
func within(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func (c *SubtitleOCRConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	if value, ok := lookup("JELEE_ENABLE_SUBTITLE_OCR"); ok {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New("JELEE_ENABLE_SUBTITLE_OCR must be a boolean")
		}
		c.Enable = enabled
	}
	if value, ok := lookup("JELEE_SUBTITLE_OCR_CACHE_ROOT"); ok {
		c.CacheRoot = value
	}
	if value, ok := lookup("JELEE_SUBTITLE_OCR_LANGUAGES"); ok {
		c.Languages = nil
		for _, language := range strings.Split(value, ",") {
			c.Languages = append(c.Languages, strings.TrimSpace(language))
		}
	}
	for _, setting := range []struct {
		name  string
		value *int
	}{
		{"JELEE_SUBTITLE_OCR_PICTURES_PER_MINUTE", &c.PicturesPerMinute},
		{"JELEE_SUBTITLE_OCR_CONCURRENCY", &c.Concurrency},
		{"JELEE_SUBTITLE_OCR_QUEUE_SIZE", &c.QueueSize},
	} {
		if value, ok := lookup(setting.name); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return errors.New(setting.name + " must be an integer")
			}
			*setting.value = n
		}
	}
	if value, ok := lookup("JELEE_SUBTITLE_OCR_CACHE_MAX_BYTES"); ok {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return errors.New("JELEE_SUBTITLE_OCR_CACHE_MAX_BYTES must be an integer")
		}
		c.CacheMaxBytes = n
	}
	return nil
}
