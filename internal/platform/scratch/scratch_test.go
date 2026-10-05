package scratch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// fakeOwners routes liveness by PID for the duration of a test.
func fakeOwners(t *testing.T, states map[int]liveness) {
	t.Helper()
	previous := processState
	processState = func(o owner) liveness { return states[o.pid] }
	t.Cleanup(func() { processState = previous })
}

func ownedName(t *testing.T, k Kind, pid int) string {
	t.Helper()
	name, err := k.NewName()
	if err != nil {
		t.Fatal(err)
	}
	// Replace the current PID so tests can model other owners.
	token := "o" + strconv.Itoa(os.Getpid()) + "-"
	if !strings.Contains(name, token) {
		t.Fatalf("name lacks owner token")
	}
	return strings.Replace(name, token, "o"+strconv.Itoa(pid)+"-", 1)
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestNameRoundTripAndPrivateDirectory(t *testing.T) {
	for _, k := range []Kind{ServiceProbe, ServiceIgnore, ProbeCheck, ImageStage} {
		name, err := k.NewName()
		if err != nil {
			t.Fatal(err)
		}
		got, who, ok := parse(name, []Kind{k})
		if !ok || got != k || who.pid != os.Getpid() || who.start == "" {
			t.Fatalf("round trip failed for %q", name)
		}
	}
	dir, err := MkdirOwned(t.TempDir(), ServiceProbe)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(dir)
	// POSIX mode bits only; Windows privacy rests on the parent's ACL.
	if err != nil || !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		t.Fatalf("directory is not private: %v", err)
	}
	if _, err := MkdirOwned(t.TempDir(), ImageStage); !errors.Is(err, ErrInvalid) {
		t.Fatal("file kinds must not create directories")
	}
}

func TestParseRejectsForeignNames(t *testing.T) {
	valid := ownedName(t, ServiceProbe, 42)
	if _, _, ok := parse(valid, []Kind{ServiceProbe}); !ok {
		t.Fatal("valid name rejected")
	}
	hexPart := valid[strings.LastIndexByte(valid, '-')+1:]
	for _, name := range []string{
		"jelee-service-probe-123456",               // legacy MkdirTemp name: owner unknown
		"jelee-service-probe-",                     //
		strings.Replace(valid, "o42-", "o042-", 1), // non-canonical PID
		strings.Replace(valid, "o42-", "o0-", 1),   //
		strings.Replace(valid, "o42-", "o-", 1),    //
		strings.Replace(valid, "o42-", "o99999999999-", 1),
		valid[:len(valid)-1], // short nonce
		valid + "0",          // long nonce
		strings.Replace(valid, hexPart, strings.ToUpper(hexPart), 1),
		valid + ".partial", // wrong suffix for a directory kind
		"x" + valid,        //
		strings.Replace(valid, "jelee-service-probe-", "jelee-service-ignore-", 1), // other kind not requested
		strings.Replace(valid, "-"+hexPart, "-x-"+hexPart, 1),                      // extra field
	} {
		if _, _, ok := parse(name, []Kind{ServiceProbe}); ok {
			t.Errorf("accepted foreign name %q", name)
		}
	}
}

func TestSweepRemovesOnlyDeadOwnedLeftovers(t *testing.T) {
	const deadPID, livePID, unknownPID = 101, 202, 303
	fakeOwners(t, map[int]liveness{deadPID: dead, livePID: alive, unknownPID: unknown})
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "keep.txt")
	mustWrite(t, outsideFile)

	deadDir := filepath.Join(root, ownedName(t, ServiceProbe, deadPID))
	mustWrite(t, filepath.Join(deadDir, "run-1", "nested", "out.bin"))
	mustWrite(t, filepath.Join(deadDir, "ignore-input-2", "request.bin"))
	// Links inside a leftover are removed as links, never followed.
	if err := os.Symlink(outside, filepath.Join(deadDir, "run-1", "escape-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(deadDir, "escape-file")); err != nil {
		t.Fatal(err)
	}
	deadFile := filepath.Join(root, ownedName(t, ImageStage, deadPID))
	mustWrite(t, deadFile)
	liveDir := filepath.Join(root, ownedName(t, ServiceIgnore, livePID))
	mustWrite(t, filepath.Join(liveDir, "run-1", "x"))
	liveFile := filepath.Join(root, ownedName(t, ImageStage, livePID))
	mustWrite(t, liveFile)
	unknownDir := filepath.Join(root, ownedName(t, ProbeCheck, unknownPID))
	mustWrite(t, filepath.Join(unknownDir, "health-1"))

	// Foreign objects: legacy/other names, wrong types and owned-looking links.
	foreign := []string{
		filepath.Join(root, "jelee-service-probe-123456", "x"),
		filepath.Join(root, "unrelated.txt"),
		filepath.Join(root, "image-0123.partial"),
	}
	for _, path := range foreign {
		mustWrite(t, path)
	}
	fileNamedLikeDir := filepath.Join(root, ownedName(t, ServiceProbe, deadPID))
	mustWrite(t, fileNamedLikeDir)
	dirNamedLikeFile := filepath.Join(root, ownedName(t, ImageStage, deadPID))
	mustWrite(t, filepath.Join(dirNamedLikeFile, "x"))
	linkTarget := filepath.Join(outside, ownedName(t, ServiceProbe, deadPID))
	mustWrite(t, filepath.Join(linkTarget, "keep"))
	ownedLink := filepath.Join(root, ownedName(t, ServiceProbe, deadPID))
	if err := os.Symlink(linkTarget, ownedLink); err != nil {
		t.Fatal(err)
	}

	report, err := Sweep(context.Background(), root, []Kind{ServiceProbe, ServiceIgnore, ProbeCheck, ImageStage}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if exists(deadDir) || exists(deadFile) {
		t.Fatal("dead owner leftovers survived")
	}
	for _, path := range append(foreign, liveDir, liveFile, unknownDir, fileNamedLikeDir, dirNamedLikeFile, ownedLink, outsideFile, filepath.Join(linkTarget, "keep"), filepath.Join(liveDir, "run-1", "x")) {
		if !exists(path) {
			t.Fatalf("kept object was removed")
		}
	}
	want := Report{Examined: 11, Removed: 2, Alive: 2, Unknown: 1, Foreign: 6}
	if report != want {
		t.Fatalf("report = %+v, want %+v", report, want)
	}
}

func TestSweepLimits(t *testing.T) {
	const deadPID = 7
	fakeOwners(t, map[int]liveness{deadPID: dead})
	root := t.TempDir()
	for range 5 {
		mustWrite(t, filepath.Join(root, ownedName(t, ImageStage, deadPID)))
	}
	report, err := Sweep(context.Background(), root, []Kind{ImageStage}, Limits{Removals: 2})
	if err != nil || report.Removed != 2 || !report.Incomplete {
		t.Fatalf("removal limit: %+v %v", report, err)
	}
	report, err = Sweep(context.Background(), root, []Kind{ImageStage}, Limits{Entries: 1})
	if err != nil || report.Examined != 1 || report.Removed != 1 || !report.Incomplete {
		t.Fatalf("entry limit: %+v %v", report, err)
	}

	tree := filepath.Join(root, ownedName(t, ServiceProbe, deadPID))
	for i := range 10 {
		mustWrite(t, filepath.Join(tree, "f"+strconv.Itoa(i)))
	}
	report, err = Sweep(context.Background(), root, []Kind{ServiceProbe}, Limits{TreeEntries: 3})
	if err != nil || report.Removed != 0 || !report.Incomplete || !exists(tree) {
		t.Fatalf("tree limit: %+v %v", report, err)
	}
	deep := filepath.Join(root, ownedName(t, ServiceProbe, deadPID))
	mustWrite(t, filepath.Join(deep, "a", "b", "c", "d"))
	report, err = Sweep(context.Background(), root, []Kind{ServiceProbe}, Limits{Depth: 2})
	if err != nil || !report.Incomplete || !exists(deep) {
		t.Fatalf("depth limit: %+v %v", report, err)
	}
	// Later sweeps finish the work without new limits.
	if _, err := Sweep(context.Background(), root, []Kind{ServiceProbe, ImageStage}, Limits{}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("%d leftovers remain", len(entries))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mustWrite(t, filepath.Join(root, ownedName(t, ImageStage, deadPID)))
	report, err = Sweep(ctx, root, []Kind{ImageStage}, Limits{})
	if err != nil || report.Removed != 0 || !report.Incomplete {
		t.Fatalf("cancelled sweep: %+v %v", report, err)
	}
}

func TestSweepRejectsUnsafeRoots(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	mustWrite(t, file)
	for _, root := range []string{link, file, "relative", filepath.Join(real, "missing")} {
		if _, err := Sweep(context.Background(), root, []Kind{ImageStage}, Limits{}); err == nil {
			t.Errorf("unsafe root accepted")
		} else if strings.Contains(err.Error(), string(filepath.Separator)) {
			t.Errorf("error exposes a path")
		}
	}
	if _, err := Sweep(context.Background(), real, nil, Limits{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty kinds accepted")
	}
}
