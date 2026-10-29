package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/windows"
)

// Run on a real Windows host (owner verification queue B18); on Linux these
// are only compiled.

func TestWindowsCommandLineAgreesWithSystemRules(t *testing.T) {
	argv := append([]string{`C:\Program Files\Jelee\ffprobe.exe`}, "", "a b", `a\"b`, `x\`, `"q"`, `sp ace\`, "影片")
	line, ok := WindowsCommandLine(argv)
	if !ok {
		t.Fatal("refused")
	}
	for _, argument := range argv[1:] {
		if escapeWindowsArgument(argument) != windows.EscapeArg(argument) {
			t.Fatalf("%q differs from windows.EscapeArg", argument)
		}
	}
	decoded, err := windows.DecomposeCommandLine(line)
	if err != nil || !slices.Equal(decoded, argv) {
		t.Fatalf("CommandLineToArgvW %q -> %q", line, decoded)
	}
}

func longPath(t *testing.T, value string) string {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(value)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(pointer, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 {
		t.Fatal(err)
	}
	return windows.UTF16ToString(buffer[:n])
}

func copyExecutable(t *testing.T) (string, string) {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(source) //nolint:gosec // G304: the running test binary
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	target := filepath.Join(longPath(t, t.TempDir()), "ffprobe.exe")
	output, err := os.Create(target) //nolint:gosec // G304: inside t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(output, hash), input); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return target, hex.EncodeToString(hash.Sum(nil))
}

func TestDirectLauncherPinsTheVerifiedImage(t *testing.T) {
	location, digest := copyExecutable(t)
	launcher, err := New(context.Background(), Profile{FFprobePath: location}, Policy{FFprobeSHA256: digest, RequireProtectedFiles: true})
	if err != nil || !launcher.Direct() || launcher.Executable() != location || !launcher.ProtectedFilesRequired() || !launcher.Confinement().Valid() {
		t.Fatalf("direct launcher: %v", err)
	}
	if os.WriteFile(location, []byte("replaced"), 0600) == nil {
		t.Fatal("pinned image was writable")
	}
	if os.Rename(location, location+".old") == nil || os.Rename(filepath.Dir(location), filepath.Dir(location)+"-moved") == nil {
		t.Fatal("pinned image or its directory could be renamed")
	}
	if err := launcher.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(location, []byte("replaced"), 0600); err != nil {
		t.Fatalf("close did not release the image: %v", err)
	}
}

func TestDirectLauncherRefusesWrongIdentity(t *testing.T) {
	location, digest := copyExecutable(t)
	wrong := "0" + digest[1:]
	if wrong == digest {
		wrong = "1" + digest[1:]
	}
	text := filepath.Join(filepath.Dir(location), "text", "ffprobe.exe")
	if err := os.MkdirAll(filepath.Dir(text), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("MZ but not a portable executable image at all, padded to a plausible size")
	if err := os.WriteFile(text, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	for name, input := range map[string]struct {
		profile Profile
		policy  Policy
		want    error
	}{
		"digest":    {Profile{FFprobePath: location}, Policy{FFprobeSHA256: wrong}, ErrUnavailable},
		"not pe":    {Profile{FFprobePath: text}, Policy{FFprobeSHA256: hex.EncodeToString(sum[:])}, ErrUnavailable},
		"closure":   {Profile{FFprobePath: location}, Policy{FFprobeSHA256: digest, Libraries: []PinnedFile{{Path: "/lib/x", SHA256: digest}}}, ErrInvalid},
		"linux":     {Profile{FFprobePath: "/usr/lib/jelee/ffprobe"}, Policy{FFprobeSHA256: digest}, ErrInvalid},
		"name":      {Profile{FFprobePath: filepath.Join(filepath.Dir(location), "ffmpeg.exe")}, Policy{FFprobeSHA256: digest}, ErrInvalid},
		"missing":   {Profile{FFprobePath: filepath.Join(filepath.Dir(location), "missing", "ffprobe.exe")}, Policy{FFprobeSHA256: digest}, ErrUnavailable},
		"directory": {Profile{FFprobePath: filepath.Join(filepath.Dir(location), "text")}, Policy{FFprobeSHA256: digest}, ErrInvalid},
	} {
		if launcher, err := New(context.Background(), input.profile, input.policy); err != input.want || launcher != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
