package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (string, map[string]pin, string) {
	t.Helper()
	root := t.TempDir()
	name := filepath.Join("nested", "library")
	file := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("approved bytes")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	return root, map[string]pin{name: {hex.EncodeToString(hash[:]), 32}}, file
}

func TestImagePinsEmbeddedExactClosure(t *testing.T) {
	pins, err := imagePins()
	// 16 ffprobe/glibc files plus 33 E4 files: mkvmerge, mkvextract,
	// mediainfo, 22 bundled and 3 Debian libraries, 5 notices.
	if err != nil || len(pins) != 49 {
		t.Fatal("image closure is not 8 libraries, 6 runtime notices, ffprobe and its license plus the E4 tools")
	}
	for _, name := range []string{"usr/lib/jelee/mkvtoolnix/mkvmerge", "usr/lib/jelee/mkvtoolnix/mkvextract", "usr/lib/jelee/mediainfo", "licenses/mkvtoolnix/COPYING", "licenses/mediainfo/LICENSE"} {
		if _, ok := pins[filepath.FromSlash(name)]; !ok {
			t.Fatal("missing E4 image pin " + name)
		}
	}
	if pins[filepath.FromSlash("usr/lib/jelee/ffprobe")].digest != "a5bd5e9f8d74ab2c6d7d9e2e2738ff6d67bf9613e7143e143ba8ebe9b06385fb" {
		t.Fatal("wrong ffprobe pin")
	}
	for name := range pins {
		if strings.Contains(name, "ffmpeg") || strings.Contains(name, "mkvpropedit") {
			t.Fatal("ffmpeg or mkvpropedit entered runtime image")
		}
	}
	delete(pins, filepath.FromSlash("usr/lib/jelee/ffprobe"))
	again, _ := imagePins()
	if len(again) != 49 {
		t.Fatal("mutable embedded pin map")
	}
}

func TestImageRejectsUnknownMissingBadHashAndOversize(t *testing.T) {
	for _, mode := range []string{"valid", "unknown", "unknown-directory", "missing", "empty", "oversize", "hash", "bad-pin", "zero-bound"} {
		t.Run(mode, func(t *testing.T) {
			root, pins, file := fixture(t)
			switch mode {
			case "unknown":
				if err := os.WriteFile(filepath.Join(root, "extra"), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown-directory":
				if err := os.Mkdir(filepath.Join(root, "extra"), 0700); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.WriteFile(file, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(file, []byte(strings.Repeat("x", 33)), 0600); err != nil {
					t.Fatal(err)
				}
			case "hash":
				if err := os.WriteFile(file, []byte("wrong bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "bad-pin":
				pins[filepath.Join("nested", "library")] = pin{"bad", 32}
			case "zero-bound":
				value := pins[filepath.Join("nested", "library")]
				value.maximum = 0
				pins[filepath.Join("nested", "library")] = value
			}
			err := verify(root, pins)
			if (mode == "valid") != (err == nil) {
				t.Fatalf("verify %s: %v", mode, err)
			}
		})
	}
	if verify(t.TempDir(), nil) != errInvalid {
		t.Fatal("empty identity accepted")
	}
}

func TestImageRejectsSymlinkAndDifferentOpenedIdentity(t *testing.T) {
	root, pins, file := fixture(t)
	other := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(other, []byte("approved bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyWithOpen(root, pins, func(string) (*os.File, error) { return os.Open(other) }); err != errInvalid {
		t.Fatal("different opened identity accepted")
	}
	if err := verifyWithOpen(root, pins, func(string) (*os.File, error) { return nil, errors.New("private path") }); err != errInvalid {
		t.Fatal("open error leaked")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, file); err != nil {
		t.Skip("host lacks symlink creation privilege")
	}
	if verify(root, pins) != errInvalid {
		t.Fatal("symlink accepted")
	}
}

func TestImageRejectsReplacementBetweenSnapshotAndOpen(t *testing.T) {
	root, pins, file := fixture(t)
	if err := verifyWithOpen(root, pins, func(name string) (*os.File, error) {
		// Keep the old inode alive so platforms cannot recycle its identity.
		old, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer old.Close()
		if err := os.Rename(name, name+".old"); err != nil {
			return nil, err
		}
		if err := os.WriteFile(name, []byte("approved bytes"), 0600); err != nil {
			return nil, err
		}
		return os.Open(name)
	}); err != errInvalid {
		t.Fatal("replaced file identity accepted")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}

func TestImageCheckerSubprocess(t *testing.T) {
	if os.Getenv("JELEE_IMAGE_CHECKER_UNIT_CHILD") != "1" {
		return
	}
	os.Args = []string{"runtime-image", "/private/alternate-root"}
	main()
}

func TestImageMainRejectsArgumentsWithFixedOutput(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestImageCheckerSubprocess$")
	command.Env = append(os.Environ(), "JELEE_IMAGE_CHECKER_UNIT_CHILD=1")
	output, err := command.CombinedOutput()
	var failure *exec.ExitError
	if !errors.As(err, &failure) || failure.ExitCode() != 1 || string(output) != "runtime_image_invalid\n" {
		t.Fatalf("unsafe checker main response: %v", err)
	}
}

func TestOCRPinsStayOutOfTheDefaultImage(t *testing.T) {
	pins, err := ocrPins()
	if err != nil || len(pins) < 100 || len(pins) > 128 {
		t.Fatalf("OCR pins %v %d", err, len(pins))
	}
	for _, name := range []string{"usr/lib/jelee/tesseract/tesseract", "usr/lib/jelee/tesseract/lib/libtesseract.so.5", "usr/lib/jelee/tesseract/tessdata/chi_tra.traineddata", "lib/x86_64-linux-gnu/libresolv.so.2", "licenses/tesseract/tesseract-ocr/copyright"} {
		if _, ok := pins[filepath.FromSlash(name)]; !ok {
			t.Fatal("missing OCR pin " + name)
		}
	}
	base, _ := imagePins()
	for name := range pins {
		if _, shared := base[name]; shared {
			t.Fatalf("%s is pinned by both trees", name)
		}
		if strings.Contains(name, "tesseract") {
			continue
		}
		if !strings.HasPrefix(name, filepath.FromSlash("usr/lib/jelee/tesseract/")) && name != filepath.FromSlash("lib/x86_64-linux-gnu/libresolv.so.2") && !strings.HasPrefix(name, filepath.FromSlash("licenses/tesseract/")) {
			t.Fatalf("OCR file outside its directories: %s", name)
		}
	}
	for name := range base {
		if strings.Contains(name, "tesseract") || strings.Contains(name, "tessdata") {
			t.Fatal("OCR file in the default runtime image: " + name)
		}
	}
}
