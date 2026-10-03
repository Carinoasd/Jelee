// image-fixture creates reproducible, synthetic sources for the image memory
// acceptance. It never reads operator media or downloads external fixtures.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const mediaAnchor = "Jelee synthetic image acceptance anchor\n"
const maxTemplateBytes = 16 << 20

var errFixture = errors.New("image fixture generation failed")

type templateInfo struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Format string `json:"format"`
}

type sample struct {
	RelativePath string `json:"relativePath"`
	SHA256       string `json:"sha256"`
	Bytes        int64  `json:"bytes"`
}

type manifest struct {
	Version         int                     `json:"version"`
	Count           int                     `json:"count"`
	Directories     int                     `json:"directories"`
	FileCount       int                     `json:"fileCount"`
	TotalBytes      int64                   `json:"totalBytes"`
	Counts          map[string]int          `json:"counts"`
	Templates       map[string]templateInfo `json:"templates"`
	Samples         []sample                `json:"samples"`
	NegativeFiles   int                     `json:"negativeFiles"`
	NegativeBytes   int64                   `json:"negativeBytes"`
	NegativeSamples []sample                `json:"negativeSamples"`
}

func main() {
	flags := flag.NewFlagSet("image-fixture", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "existing empty owned directory")
	count := flags.Int("count", 0, "1000 smoke or 100000 acceptance sources")
	if flags.Parse(os.Args[1:]) != nil || flags.NArg() != 0 || (*count != 1000 && *count != 100000) {
		fmt.Fprintln(os.Stderr, "invalid image fixture arguments")
		os.Exit(2)
	}
	result, err := generate(*root, *count)
	if err != nil {
		fmt.Fprintln(os.Stderr, errFixture)
		os.Exit(1)
	}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		os.Exit(1)
	}
}

// Zero-based indices 0..63 are PNG16. The remaining PNG group ends at
// max(100, count/20); all later sources are JPEG, including the warm tail.
// Each ten items share one directory. Every source is a separate regular file.
func fixtureKind(index, count int) string {
	if index < 64 {
		return "png16"
	}
	if index < max(100, count/20) {
		return "png"
	}
	return "jpeg"
}

func fixturePaths(index int, kind string) (string, string) {
	base := fmt.Sprintf("dir-%05d/clip-%06d", index/10, index)
	extension := ".png"
	if kind == "jpeg" {
		extension = ".jpg"
	}
	return base + ".mkv", base + "-poster" + extension
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > maxTemplateBytes-b.Len() {
		return 0, errFixture
	}
	return b.Buffer.Write(data)
}

func (b *boundedBuffer) WriteByte(value byte) error {
	if b.Len() >= maxTemplateBytes {
		return errFixture
	}
	return b.Buffer.WriteByte(value)
}

func makeTemplate(kind string) ([]byte, templateInfo, error) {
	var picture image.Image
	var encoded boundedBuffer
	var err error
	switch kind {
	case "media":
		data := []byte(mediaAnchor)
		return data, describe(data, 0, 0, "anchor"), nil
	case "jpeg":
		p := image.NewRGBA(image.Rect(0, 0, 640, 960))
		for y := 0; y < 960; y++ {
			for x := 0; x < 640; x++ {
				p.SetRGBA(x, y, color.RGBA{R: uint8(x / 3), G: uint8(y / 4), B: uint8((x + y) / 7), A: 255})
			}
		}
		picture = p
		err = jpeg.Encode(&encoded, picture, &jpeg.Options{Quality: 90})
	case "png":
		p := image.NewNRGBA(image.Rect(0, 0, 256, 384))
		for y := 0; y < 384; y++ {
			for x := 0; x < 256; x++ {
				alpha := uint8(255)
				if x < 64 {
					alpha = 0
				}
				p.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y / 2), B: uint8((x + y) / 3), A: alpha})
			}
		}
		picture = p
		err = png.Encode(&encoded, picture)
	case "png16":
		p := image.NewNRGBA64(image.Rect(0, 0, 4096, 2560))
		for y := 0; y < 2560; y++ {
			for x := 0; x < 4096; x++ {
				p.SetNRGBA64(x, y, color.NRGBA64{R: uint16(x * 16), G: uint16(y * 24), B: uint16((x + y) * 8), A: 65535})
			}
		}
		picture = p
		err = png.Encode(&encoded, picture)
	default:
		return nil, templateInfo{}, errFixture
	}
	if err != nil || encoded.Len() == 0 {
		return nil, templateInfo{}, errFixture
	}
	data := encoded.Bytes()
	format := "png"
	if kind == "jpeg" {
		format = "jpeg"
	}
	return data, describe(data, picture.Bounds().Dx(), picture.Bounds().Dy(), format), nil
}

func describe(data []byte, width, height int, format string) templateInfo {
	digest := sha256.Sum256(data)
	return templateInfo{SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(data)), Width: width, Height: height, Format: format}
}

func generate(path string, count int) (manifest, error) {
	result := manifest{Version: 1, Count: count, Directories: (count + 9) / 10, FileCount: count * 2,
		Counts: map[string]int{"jpeg": 0, "png": 0, "png16": 0}, Templates: make(map[string]templateInfo)}
	if (count != 1000 && count != 100000) || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return manifest{}, errFixture
	}
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return manifest{}, errFixture
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil || realPath != path {
		return manifest{}, errFixture
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return manifest{}, errFixture
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return manifest{}, errFixture
	}
	held, statErr := directory.Stat()
	entries, readErr := directory.ReadDir(1)
	closeErr := directory.Close()
	if statErr != nil || !os.SameFile(before, held) || !privateFixtureRoot(held) || len(entries) != 0 || readErr != io.EOF || closeErr != nil {
		return manifest{}, errFixture
	}
	data := make(map[string][]byte)
	for _, kind := range []string{"media", "jpeg", "png", "png16"} {
		var err error
		data[kind], result.Templates[kind], err = makeTemplate(kind)
		if err != nil {
			return manifest{}, errFixture
		}
	}
	selected := sampleIndices(count)
	for index := 0; index < count; index++ {
		dir := fmt.Sprintf("dir-%05d", index/10)
		if index%10 == 0 && root.Mkdir(dir, 0700) != nil {
			return manifest{}, errFixture
		}
		kind := fixtureKind(index, count)
		media, poster := fixturePaths(index, kind)
		for _, file := range []struct{ path, kind string }{{media, "media"}, {poster, kind}} {
			writer, err := root.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return manifest{}, errFixture
			}
			n, writeErr := writer.Write(data[file.kind])
			modeErr := writer.Chmod(0444)
			closeErr := writer.Close()
			if writeErr != nil || n != len(data[file.kind]) || modeErr != nil || closeErr != nil {
				return manifest{}, errFixture
			}
			result.TotalBytes += int64(n)
			if selected[index] {
				result.Samples = append(result.Samples, sample{RelativePath: file.path, SHA256: result.Templates[file.kind].SHA256, Bytes: int64(n)})
			}
		}
		result.Counts[kind]++
		if index%10 == 9 && root.Chmod(dir, 0555) != nil {
			return manifest{}, errFixture
		}
	}
	if err := writeNegatives(root, data, &result); err != nil {
		return manifest{}, errFixture
	}
	if root.Chmod(".", 0555) != nil {
		return manifest{}, errFixture
	}
	return result, nil
}

// These deliberately invalid images never count toward the successful workload.
// The dimensions case has a valid PNG header/CRC but inconsistent small IDAT;
// its purpose is to prove pre-decode budget rejection, not successful decoding.
func negativeData(templates map[string][]byte) map[string][]byte {
	dimensions := append([]byte(nil), templates["png"]...)
	binary.BigEndian.PutUint32(dimensions[16:20], 16384)
	binary.BigEndian.PutUint32(dimensions[20:24], 16384)
	binary.BigEndian.PutUint32(dimensions[29:33], crc32.ChecksumIEEE(dimensions[12:29]))
	return map[string][]byte{
		"unsupported/clip-poster.jpg":          []byte("GIF89a unsupported synthetic image fixture"),
		"corrupt/clip-poster.jpg":              append([]byte(nil), templates["jpeg"][:64]...),
		"oversized-source/clip-poster.jpg":     make([]byte, maxTemplateBytes+1),
		"oversized-dimensions/clip-poster.png": dimensions,
	}
}

func writeNegatives(root *os.Root, templates map[string][]byte, result *manifest) error {
	if root.Mkdir("negative", 0700) != nil {
		return errFixture
	}
	negatives := negativeData(templates)
	paths := make([]string, 0, len(negatives))
	for path := range negatives {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		dir := "negative/" + filepath.ToSlash(filepath.Dir(path))
		if root.Mkdir(dir, 0700) != nil {
			return errFixture
		}
		for _, file := range []struct {
			path string
			data []byte
		}{
			{dir + "/clip.mkv", templates["media"]}, {"negative/" + path, negatives[path]},
		} {
			writer, err := root.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return errFixture
			}
			n, writeErr := writer.Write(file.data)
			modeErr := writer.Chmod(0444)
			closeErr := writer.Close()
			if n != len(file.data) || writeErr != nil || modeErr != nil || closeErr != nil {
				return errFixture
			}
			info := describe(file.data, 0, 0, "negative")
			result.NegativeSamples = append(result.NegativeSamples, sample{RelativePath: file.path, SHA256: info.SHA256, Bytes: info.Bytes})
			result.NegativeFiles++
			result.NegativeBytes += info.Bytes
		}
		if root.Chmod(dir, 0555) != nil {
			return errFixture
		}
	}
	return root.Chmod("negative", 0555)
}

func sampleIndices(count int) map[int]bool {
	boundary := max(100, count/20)
	indices := []int{0, 63, 64, boundary - 1, boundary, count - 65, count - 64, count - 1}
	for n := 1; n <= 16; n++ {
		indices = append(indices, (count-1)*n/17)
	}
	sort.Ints(indices)
	selected := make(map[int]bool)
	for _, index := range indices {
		selected[index] = true
	}
	return selected
}
