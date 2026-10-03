package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestFixtureDistributionAndBoundedSamples(t *testing.T) {
	for _, count := range []int{1000, 100000} {
		kinds := make(map[string]int)
		for i := 0; i < count; i++ {
			kind := fixtureKind(i, count)
			kinds[kind]++
			media, poster := fixturePaths(i, kind)
			if !filepath.IsLocal(media) || !filepath.IsLocal(poster) || media == poster {
				t.Fatal("fixture paths must be distinct and local")
			}
			if i >= count-64 && kind != "jpeg" {
				t.Fatal("warm tail must be JPEG")
			}
		}
		pngCount := max(100, count/20)
		if kinds["png16"] != 64 || kinds["png"] != pngCount-64 || kinds["jpeg"] != count-pngCount {
			t.Fatal("fixture distribution changed")
		}
		selected := sampleIndices(count)
		if len(selected) > 32 || !selected[0] || !selected[63] || !selected[64] || !selected[pngCount-1] || !selected[pngCount] || !selected[count-1] {
			t.Fatal("samples must cover format boundaries in bounded memory")
		}
	}
}

func TestFixtureTemplatesAreRealImages(t *testing.T) {
	for _, kind := range []string{"jpeg", "png", "png16"} {
		t.Run(kind, func(t *testing.T) {
			data, info, err := makeTemplate(kind)
			if err != nil || len(data) == 0 || len(data) > maxTemplateBytes || int64(len(data)) != info.Bytes {
				t.Fatal("encode bounded image template")
			}
			config, format, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || format != info.Format || config.Width != info.Width || config.Height != info.Height {
				t.Fatal("encoded image dimensions or format differ")
			}
			var decoded image.Image
			if format == "jpeg" {
				decoded, err = jpeg.Decode(bytes.NewReader(data))
			} else {
				decoded, err = png.Decode(bytes.NewReader(data))
			}
			if err != nil || decoded.Bounds().Dx() != info.Width || decoded.Bounds().Dy() != info.Height {
				t.Fatal("template must fully decode")
			}
			if kind == "png" {
				_, _, _, alpha := decoded.At(10, 10).RGBA()
				if alpha != 0 {
					t.Fatal("PNG fixture lost transparent region")
				}
			}
			if kind == "png16" {
				if config.ColorModel != color.RGBA64Model && config.ColorModel != color.NRGBA64Model {
					t.Fatal("large fixture must exercise a 16-bit decoder")
				}
			}
		})
	}
}

func TestFixtureRefusesExistingContentAndInvalidArguments(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "keep.txt")
	if os.WriteFile(path, []byte("keep"), 0600) != nil {
		t.Fatal("create existing fixture")
	}
	for _, count := range []int{-1, 0, 42, 1000} {
		if _, err := generate(root, count); err == nil {
			t.Fatal("must refuse invalid count or existing directory content")
		}
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatal("existing file modified")
	}
	for _, root := range []string{".", "", "../escape"} {
		if _, err := generate(root, 1000); err == nil {
			t.Fatal("relative destination accepted")
		}
	}
}

func TestFixtureEncodedOutputHasFixedLimit(t *testing.T) {
	var output boundedBuffer
	if n, err := output.Write(make([]byte, maxTemplateBytes)); err != nil || n != maxTemplateBytes {
		t.Fatal("bounded write")
	}
	if n, err := output.Write([]byte{1}); err == nil || n != 0 || output.Len() != maxTemplateBytes {
		t.Fatal("overflow must not append bytes")
	}
	if err := output.WriteByte(1); err == nil || output.Len() != maxTemplateBytes {
		t.Fatal("JPEG byte writer must enforce the same output bound")
	}
}

func TestFixtureNegativeDimensionsArePrecheckOnly(t *testing.T) {
	jpegData, _, err := makeTemplate("jpeg")
	if err != nil {
		t.Fatal("JPEG template")
	}
	pngData, _, err := makeTemplate("png")
	if err != nil {
		t.Fatal("PNG template")
	}
	data := negativeData(map[string][]byte{"jpeg": jpegData, "png": pngData})
	large := data["oversized-dimensions/clip-poster.png"]
	config, err := png.DecodeConfig(bytes.NewReader(large))
	if err != nil || config.Width != 16384 || config.Height != 16384 {
		t.Fatal("oversize header must be accepted by dimension precheck")
	}
	if len(data) != 4 || len(data["oversized-source/clip-poster.jpg"]) != maxTemplateBytes+1 {
		t.Fatal("negative fixture count or byte limit changed")
	}
	if _, err := jpeg.Decode(bytes.NewReader(data["corrupt/clip-poster.jpg"])); err == nil {
		t.Fatal("truncated fixture must not decode")
	}
}
