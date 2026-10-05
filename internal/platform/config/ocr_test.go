package config

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestSubtitleOCRConfiguration(t *testing.T) {
	// Validation requires clean roots; the Windows runner's TMP mixes separators.
	matroska, ocr := filepath.Clean(t.TempDir()), filepath.Clean(t.TempDir())
	base := map[string]string{"JELEE_DATABASE_URL": "postgres://localhost/jelee", "JELEE_ENABLE_CATALOG": "true", "JELEE_ENABLE_DIRECT": "true",
		"JELEE_ENABLE_MATROSKA_EXTRACTION": "true", "JELEE_MATROSKA_CACHE_ROOT": matroska}
	load := func(extra map[string]string) (Config, error) {
		values := map[string]string{}
		for k, v := range base {
			values[k] = v
		}
		for k, v := range extra {
			values[k] = v
		}
		return LoadWith(func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	}
	c, err := load(nil)
	if err != nil || c.SubtitleOCR.Enable || !slices.Equal(c.SubtitleOCR.Languages, []string{"eng"}) || c.SubtitleOCR.PicturesPerMinute != 60 || c.SubtitleOCR.Concurrency != 1 || c.SubtitleOCR.QueueSize != 16 || c.SubtitleOCR.CacheMaxBytes != 256<<20 {
		t.Fatalf("default: %v %+v", err, c.SubtitleOCR)
	}
	enabled := map[string]string{"JELEE_ENABLE_SUBTITLE_OCR": "true", "JELEE_SUBTITLE_OCR_CACHE_ROOT": ocr, "JELEE_SUBTITLE_OCR_LANGUAGES": "chi_tra, eng",
		"JELEE_SUBTITLE_OCR_PICTURES_PER_MINUTE": "120", "JELEE_SUBTITLE_OCR_CONCURRENCY": "2", "JELEE_SUBTITLE_OCR_QUEUE_SIZE": "4", "JELEE_SUBTITLE_OCR_CACHE_MAX_BYTES": "33554432"}
	c, err = load(enabled)
	if err != nil || !c.SubtitleOCR.Enable || c.SubtitleOCR.CacheRoot != ocr || !slices.Equal(c.SubtitleOCR.Languages, []string{"chi_tra", "eng"}) ||
		c.SubtitleOCR.PicturesPerMinute != 120 || c.SubtitleOCR.Concurrency != 2 || c.SubtitleOCR.QueueSize != 4 || c.SubtitleOCR.CacheMaxBytes != 32<<20 {
		t.Fatalf("enabled: %v %+v", err, c.SubtitleOCR)
	}
	with := func(changes map[string]string) map[string]string {
		values := map[string]string{}
		for k, v := range enabled {
			values[k] = v
		}
		for k, v := range changes {
			values[k] = v
		}
		return values
	}
	for name, extra := range map[string]map[string]string{
		"flag":          {"JELEE_ENABLE_SUBTITLE_OCR": "maybe"},
		"rate syntax":   {"JELEE_SUBTITLE_OCR_PICTURES_PER_MINUTE": "fast"},
		"bytes syntax":  {"JELEE_SUBTITLE_OCR_CACHE_MAX_BYTES": "lots"},
		"no matroska":   with(map[string]string{"JELEE_ENABLE_MATROSKA_EXTRACTION": "false"}),
		"no root":       with(map[string]string{"JELEE_SUBTITLE_OCR_CACHE_ROOT": ""}),
		"relative":      with(map[string]string{"JELEE_SUBTITLE_OCR_CACHE_ROOT": "ocr"}),
		"shared root":   with(map[string]string{"JELEE_SUBTITLE_OCR_CACHE_ROOT": matroska}),
		"nested root":   with(map[string]string{"JELEE_SUBTITLE_OCR_CACHE_ROOT": filepath.Join(matroska, "ocr")}),
		"parent root":   with(map[string]string{"JELEE_SUBTITLE_OCR_CACHE_ROOT": filepath.Dir(matroska)}),
		"language":      with(map[string]string{"JELEE_SUBTITLE_OCR_LANGUAGES": "eng,fra"}),
		"duplicate":     with(map[string]string{"JELEE_SUBTITLE_OCR_LANGUAGES": "eng,eng"}),
		"empty":         with(map[string]string{"JELEE_SUBTITLE_OCR_LANGUAGES": ""}),
		"rate zero":     with(map[string]string{"JELEE_SUBTITLE_OCR_PICTURES_PER_MINUTE": "0"}),
		"rate high":     with(map[string]string{"JELEE_SUBTITLE_OCR_PICTURES_PER_MINUTE": "6001"}),
		"concurrency":   with(map[string]string{"JELEE_SUBTITLE_OCR_CONCURRENCY": "5"}),
		"no processes":  with(map[string]string{"JELEE_SUBTITLE_OCR_CONCURRENCY": "0"}),
		"queue":         with(map[string]string{"JELEE_SUBTITLE_OCR_QUEUE_SIZE": "257"}),
		"small cache":   with(map[string]string{"JELEE_SUBTITLE_OCR_CACHE_MAX_BYTES": "1024"}),
		"queue syntax":  with(map[string]string{"JELEE_SUBTITLE_OCR_QUEUE_SIZE": "x"}),
		"concur syntax": with(map[string]string{"JELEE_SUBTITLE_OCR_CONCURRENCY": "x"}),
	} {
		if _, err := load(extra); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
