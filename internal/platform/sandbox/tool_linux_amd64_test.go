package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/sys/unix"

	"github.com/MoYuanCN/Jelee/tools"
)

// runToolHelper starts the test helper's tool entry with a private working
// directory and the given stdin file, like the process runner does.
func runToolHelper(t *testing.T, helper string, launcher *ToolLauncher, policy ToolPolicy, extraction Extraction, input, dir string) ([]byte, error) {
	t.Helper()
	arguments, err := launcher.HelperArguments(extraction)
	if err != nil {
		t.Fatal(err)
	}
	registration, _ := json.Marshal(map[string]any{"Profile": ToolProfile{Mode: launcher.Mode(), Path: launcher.profile.Path}, "Policy": policy})
	file, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, helper, "--test-tool-helper", arguments[1])
	command.Env = []string{"JELEE_TEST_TOOL_POLICY=" + string(registration)}
	command.Stdin = file
	command.Dir = dir
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if err != nil {
		t.Logf("tool helper stderr: %s", stderr.String())
	}
	return []byte(stdout.String()), err
}

// extractFixture returns the fake mkvextract (the attack helper under the
// mode's fixed name). The required native container's only writable
// directory (TMPDIR) is mounted noexec, where the final execveat fails with
// EACCES after every restriction is applied, so that proof image ships the
// copy; other runs copy the helper into a temporary directory.
func extractFixture(t *testing.T, helper string) string {
	t.Helper()
	path := os.Getenv("JELEE_SANDBOX_EXTRACT_FIXTURE")
	if path == "" {
		path = filepath.Join(t.TempDir(), "mkvextract")
		copyFixture(t, helper, path)
	}
	var mount unix.Statfs_t
	if err := unix.Statfs(path, &mount); err != nil || mount.Flags&unix.ST_NOEXEC != 0 {
		t.Fatal("fake mkvextract must be on an exec-capable mount; set JELEE_SANDBOX_EXTRACT_FIXTURE")
	}
	return path
}

func TestNativeToolSandboxConfinesExtraction(t *testing.T) {
	requireNative(t)
	helper := fixtureHelper(t)
	requireNativeThreadBudget(t)
	fake := extractFixture(t, helper)
	policy := ToolPolicy{ExecutableSHA256: fileDigest(t, fake)}
	launcher, err := NewTool(context.Background(), ToolProfile{Mode: ToolExtract, Path: fake}, policy)
	if err != nil {
		t.Fatalf("verified fixture preparation: %v", err)
	}
	root := t.TempDir()
	private := filepath.Join(root, "private.txt")
	if err := os.WriteFile(private, []byte("synthetic secret"), 0600); err != nil {
		t.Fatal(err)
	}
	request, _ := json.Marshal(map[string]string{"OutsideFile": private, "WriteFile": filepath.Join(root, "escaped")})
	input := filepath.Join(root, "input.json")
	if err := os.WriteFile(input, request, 0600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	output, err := runToolHelper(t, helper, launcher, policy, Extraction{Tracks: []int{0}}, input, work)
	if err != nil {
		t.Fatalf("sandboxed fake extraction failed: %v", err)
	}
	var result map[string]bool
	if json.Unmarshal(output, &result) != nil || len(result) != 9 {
		t.Fatalf("unexpected probe report %q", output)
	}
	for name, ok := range result {
		if !ok {
			t.Errorf("tool sandbox check failed: %s", name)
		}
	}
	if data, err := os.ReadFile(filepath.Join(work, "t0")); err != nil || string(data) != "extracted" {
		t.Fatal("extraction output missing from the private directory")
	}
	if _, err := os.Stat(filepath.Join(root, "escaped")); !os.IsNotExist(err) {
		t.Fatal("extraction wrote outside its private directory")
	}
	// The same fake under another mode is refused by name before any exec.
	if _, err := NewTool(context.Background(), ToolProfile{Mode: ToolIdentify, Path: fake}, policy); err != ErrInvalid {
		t.Fatal("executable name not bound to the mode")
	}
	// A changed executable digest is refused by the helper itself.
	wrong := policy
	wrong.ExecutableSHA256 = strings.Repeat("0", 64)
	if _, err := runToolHelper(t, helper, launcher, wrong, Extraction{Tracks: []int{0}}, input, t.TempDir()); err == nil {
		t.Fatal("helper executed a binary whose digest differs from its policy")
	}
}

// hostRuntimePolicy builds an explicit developer policy: the project's
// pinned tool files plus this host's glibc, hashed now. It is used only when
// JELEE_MATROSKA_HOST_RUNTIME=true; production uses the pinned image runtime.
func hostRuntimePolicy(t *testing.T, root, name string) (ToolProfile, ToolPolicy) {
	t.Helper()
	spec, err := tools.MatroskaToolSpec("linux-amd64", name)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, filepath.FromSlash(spec.Executable.Path))
	if _, err := os.Stat(executable); err != nil {
		if os.Getenv("JELEE_REQUIRE_MEDIA_TOOL_TESTS") == "true" {
			t.Fatalf("%s is not installed (make bootstrap-matroska)", name)
		}
		t.Skipf("%s is not installed; run make bootstrap-matroska", name)
	}
	policy := ToolPolicy{ExecutableSHA256: spec.Executable.SHA256}
	glibc := map[string]bool{"ld-linux-x86-64.so.2": true, "libc.so.6": true, "libm.so.6": true, "libdl.so.2": true, "libpthread.so.0": true, "librt.so.1": true, "libgcc_s.so.1": true, "libmvec.so.1": true}
	for _, library := range spec.Libraries {
		base := filepath.Base(library.ContainerPath)
		path := filepath.Join(root, filepath.FromSlash(library.Path))
		if glibc[base] {
			candidate := "/usr/lib/x86_64-linux-gnu/" + base
			if base == "ld-linux-x86-64.so.2" {
				candidate, err = filepath.EvalSymlinks("/lib64/ld-linux-x86-64.so.2")
				if err != nil {
					t.Fatal(err)
				}
			}
			path = candidate
		}
		policy.Libraries = append(policy.Libraries, PinnedFile{Path: path, SHA256: fileDigest(t, path)})
	}
	mode := map[string]ToolMode{"mediainfo": ToolMediaInfo, "mkvmerge": ToolIdentify, "mkvextract": ToolExtract}[name]
	return ToolProfile{Mode: mode, Path: executable}, policy
}

func TestRealMatroskaToolsInSandboxWithExplicitHostRuntime(t *testing.T) {
	if os.Getenv("JELEE_MATROSKA_HOST_RUNTIME") != "true" {
		t.Skip("real mkvtoolnix/MediaInfo need JELEE_MATROSKA_HOST_RUNTIME=true; host glibc is never trusted implicitly")
	}
	requireNative(t)
	helper := fixtureHelper(t)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	source := os.Getenv("JELEE_MATROSKA_FIXTURE")
	if source == "" {
		t.Skip("JELEE_MATROSKA_FIXTURE names a generated Matroska fixture (tools/gen-fixtures subtitles-fonts.mkv)")
	}
	before := fileDigest(t, source)
	for _, name := range []string{"mkvmerge", "mkvextract", "mediainfo"} {
		t.Run(name, func(t *testing.T) {
			profile, policy := hostRuntimePolicy(t, root, name)
			launcher, err := NewTool(context.Background(), profile, policy)
			if err != nil {
				t.Fatalf("real %s verification failed: %v", name, err)
			}
			extraction := Extraction{}
			if name == "mkvextract" {
				extraction = Extraction{Tracks: []int{1, 2}, Attachments: []int{1}}
			}
			work := t.TempDir()
			output, err := runToolHelper(t, helper, launcher, policy, extraction, source, work)
			if err != nil {
				t.Fatalf("real %s in sandbox failed: %v", name, err)
			}
			switch name {
			case "mkvmerge":
				var identified struct {
					Container struct{ Recognized bool } `json:"container"`
					Tracks    []json.RawMessage         `json:"tracks"`
				}
				if json.Unmarshal(output, &identified) != nil || !identified.Container.Recognized || len(identified.Tracks) < 3 {
					t.Fatalf("unexpected identification %s", output)
				}
			case "mediainfo":
				if !strings.Contains(string(output), `"@ref":"/proc/self/fd/0"`) || !strings.Contains(string(output), "Matroska") {
					t.Fatalf("unexpected MediaInfo output %s", output)
				}
			case "mkvextract":
				for _, file := range []string{"t1", "t2", "a1"} {
					if info, err := os.Stat(filepath.Join(work, file)); err != nil || info.Size() == 0 {
						t.Fatalf("missing extracted %s", file)
					}
				}
			}
		})
	}
	if fileDigest(t, source) != before {
		t.Fatal("a tool changed the source bytes")
	}
}

// ocrToolsRoot is the directory whose .tools holds the bootstrapped OCR
// runtime: JELEE_OCR_TOOLS_ROOT, or the repository root.
func ocrToolsRoot(t *testing.T) string {
	t.Helper()
	if root := os.Getenv("JELEE_OCR_TOOLS_ROOT"); root != "" {
		return root
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// hostOCRPolicy builds an explicit developer policy for Tesseract: the
// project's pinned files plus this host's glibc and libresolv, hashed now.
// It is used only when JELEE_OCR_HOST_RUNTIME=true.
func hostOCRPolicy(t *testing.T, root string, languages ...string) (ToolProfile, ToolPolicy) {
	t.Helper()
	spec, err := tools.OCRToolSpec("linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, filepath.FromSlash(spec.Executable.Path))
	if _, err := os.Stat(executable); err != nil {
		if os.Getenv("JELEE_REQUIRE_MEDIA_TOOL_TESTS") == "true" {
			t.Fatal("tesseract is not installed (make bootstrap-ocr)")
		}
		t.Skip("tesseract is not installed; run make bootstrap-ocr")
	}
	policy := ToolPolicy{ExecutableSHA256: spec.Executable.SHA256}
	glibc := map[string]bool{"ld-linux-x86-64.so.2": true, "libc.so.6": true, "libm.so.6": true, "libgcc_s.so.1": true, "libresolv.so.2": true}
	for _, library := range spec.Libraries {
		base := filepath.Base(library.ContainerPath)
		path := filepath.Join(root, filepath.FromSlash(library.Path))
		if glibc[base] {
			path = "/usr/lib/x86_64-linux-gnu/" + base
			if base == "ld-linux-x86-64.so.2" {
				path, err = filepath.EvalSymlinks("/lib64/ld-linux-x86-64.so.2")
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		policy.Libraries = append(policy.Libraries, PinnedFile{Path: path, SHA256: fileDigest(t, path)})
	}
	for _, language := range languages {
		file := spec.Languages[language]
		policy.DataFiles = append(policy.DataFiles, PinnedFile{Path: filepath.Join(root, filepath.FromSlash(file.Path)), SHA256: file.SHA256})
	}
	return ToolProfile{Mode: ToolOCR, Path: executable}, policy
}

// writeTextPGM renders black text on white as a binary PGM.
func writeTextPGM(t *testing.T, path, text string) {
	t.Helper()
	parsed, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 40, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.NewGray(image.Rect(0, 0, 32+font.MeasureString(face, text).Ceil(), 80))
	draw.Draw(canvas, canvas.Bounds(), image.White, image.Point{}, draw.Src)
	drawer := font.Drawer{Dst: canvas, Src: image.Black, Face: face, Dot: fixed.P(16, 54)}
	drawer.DrawString(text)
	header := fmt.Sprintf("P5\n%d %d\n255\n", canvas.Bounds().Dx(), canvas.Bounds().Dy())
	if err := os.WriteFile(path, append([]byte(header), canvas.Pix...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRealTesseractInSandboxWithExplicitHostRuntime(t *testing.T) {
	if os.Getenv("JELEE_OCR_HOST_RUNTIME") != "true" {
		t.Skip("real Tesseract needs JELEE_OCR_HOST_RUNTIME=true; host glibc is never trusted implicitly")
	}
	requireNative(t)
	helper := fixtureHelper(t)
	root := ocrToolsRoot(t)
	profile, policy := hostOCRPolicy(t, root, "eng", "chi_tra")
	launcher, err := NewTool(context.Background(), profile, policy)
	if err != nil {
		t.Fatalf("real tesseract verification failed: %v", err)
	}
	if got := launcher.Languages(); !slices.Equal(got, []string{"eng", "chi_tra"}) {
		t.Fatalf("languages %q", got)
	}
	input := filepath.Join(t.TempDir(), "picture.pgm")
	writeTextPGM(t, input, "Jelee reads 42 subtitles")
	before := fileDigest(t, input)
	work := t.TempDir()
	output, err := runToolHelper(t, helper, launcher, policy, Extraction{Languages: []string{"eng"}}, input, work)
	if err != nil {
		t.Fatalf("real tesseract in sandbox failed: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "Jelee reads 42 subtitles" {
		t.Fatalf("recognized %q", got)
	}
	if fileDigest(t, input) != before {
		t.Fatal("tesseract changed its input")
	}
	if entries, err := os.ReadDir(work); err != nil || len(entries) != 0 {
		t.Fatal("tesseract wrote into its working directory")
	}
	// A language whose data is not granted is refused before exec.
	if _, err := launcher.HelperArguments(Extraction{Languages: []string{"jpn"}}); err != ErrInvalid {
		t.Fatal("ungranted language accepted")
	}
	// A changed language data digest is refused by the helper itself.
	wrong := policy
	wrong.DataFiles = slices.Clone(policy.DataFiles)
	wrong.DataFiles[0].SHA256 = strings.Repeat("0", 64)
	if _, err := runToolHelper(t, helper, launcher, wrong, Extraction{Languages: []string{"eng"}}, input, t.TempDir()); err == nil {
		t.Fatal("helper accepted language data whose digest differs from its policy")
	}
}
