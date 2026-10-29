//go:build windows || linux

package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// createFixture is a directory without the target, the document to create and
// the callbacks a repository would provide. The callbacks record what they saw.
type createFixture struct {
	t           *testing.T
	path        string
	directory   *os.Root
	replacement *Document
	events      []string
	plan        *nfoCommitFilePlan
	ready       *nfoCommitFiles
	checkpoint  *nfoCommitFiles
}

const createTarget = "movie.nfo"

func newCreateFixture(t *testing.T) *createFixture {
	t.Helper()
	path := t.TempDir()
	directory := lockTestRoot(t, path)
	ctx := context.Background()
	replacement, err := createBaseDocument(ctx, "Movie")
	if err != nil {
		t.Fatal(err)
	}
	opts := TextEditOptions{CreateMissing: true}
	if replacement, err = replacement.WithTextOptions(ctx, 0, "title", "created title", DefaultMaxBytes, opts); err != nil {
		t.Fatal(err)
	}
	if replacement, err = replacement.EnsureID(ctx, 0, DefaultMaxBytes, opts); err != nil {
		t.Fatal(err)
	}
	return &createFixture{t: t, path: path, directory: directory, replacement: replacement}
}

func (f *createFixture) persistence(attempt uint8) nfoCommitFilePersistence {
	return nfoCommitFilePersistence{
		attempt: attempt,
		plan: func(_ context.Context, plan nfoCommitFilePlan) error {
			f.events = append(f.events, "plan")
			f.plan = &plan
			return nil
		},
		progress: func(_ context.Context, files nfoCommitFiles) error {
			f.events = append(f.events, "checkpoint")
			f.checkpoint = &files
			return nil
		},
		ready: func(_ context.Context, files nfoCommitFiles) error {
			f.events = append(f.events, "ready")
			f.ready = &files
			return nil
		},
	}
}

func (f *createFixture) names() []string {
	f.t.Helper()
	return directoryNamesWithoutLock(f.t, f.path)
}

func directoryNamesWithoutLock(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".lock") {
			names = append(names, entry.Name())
		}
	}
	return names
}

func (f *createFixture) stage(ops nfoWriteOperations) (*nfoCommitFiles, error) {
	return prepareNFOCreateFiles(context.Background(), f.directory, createTarget, f.replacement, commitFileToken(), f.persistence(0), ops)
}

func (f *createFixture) mustStage() *nfoCommitFiles {
	f.t.Helper()
	files, err := f.stage(nativeNFOWriteOperations())
	if err != nil || files == nil {
		f.t.Fatal("create staging failed", err)
	}
	return files
}

// settle runs the link step; the recorded phases and the link calls are returned.
func (f *createFixture) settle(files *nfoCommitFiles, backedUp bool, ops nfoWriteOperations) ([]nfoSettlePhase, nfoSettlePhase, error) {
	var phases []nfoSettlePhase
	progress := func(_ context.Context, phase nfoSettlePhase) error {
		phases = append(phases, phase)
		return nil
	}
	phase, err := settleNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, backedUp, progress, ops)
	return phases, phase, err
}

func (f *createFixture) targetExists() bool {
	_, err := os.Lstat(filepath.Join(f.path, createTarget))
	return err == nil
}

func (f *createFixture) readTarget() []byte {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.path, createTarget))
	if err != nil {
		f.t.Fatal(err)
	}
	return data
}

func sameObject(t *testing.T, a, b string) bool {
	t.Helper()
	left, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	right, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(left, right)
}

func TestPrepareNFOCreateFilesRetainsOnlyOutputAndPin(t *testing.T) {
	f := newCreateFixture(t)
	ops := nativeNFOWriteOperations()
	// Nothing but the original directory exists when the plan is persisted.
	plan := f.persistence(0)
	plan.plan = func(_ context.Context, p nfoCommitFilePlan) error {
		if names := f.names(); len(names) != 0 {
			t.Fatal("file side effect happened before plan persistence", names)
		}
		if !p.create() || p.target != (nfoNativeIdentity{}) || p.parent == (nfoNativeIdentity{}) || p.attempt != 0 {
			t.Fatal("create plan carries a target or the wrong version")
		}
		f.events = append(f.events, "plan")
		return nil
	}
	files, err := prepareNFOCreateFiles(context.Background(), f.directory, createTarget, f.replacement, commitFileToken(), plan, ops)
	if err != nil || files == nil || !slices.Equal(f.events, []string{"plan", "checkpoint", "ready"}) {
		t.Fatal("create staging failed", err, f.events)
	}
	if files.rollback != (nfoNativeIdentity{}) || files.output == (nfoNativeIdentity{}) || f.checkpoint.output != files.output {
		t.Fatal("create evidence names a rollback object or lost the output")
	}
	if f.targetExists() {
		t.Fatal("staging created the target")
	}
	names := files.plan.names()
	want := []string{names[1], names[2]}
	got := f.names()
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatal("create retained names other than output and pin", got)
	}
	for _, name := range want {
		data, err := os.ReadFile(filepath.Join(f.path, name))
		if err != nil || !bytes.Equal(data, f.replacement.original) {
			t.Fatal("staged bytes differ", name)
		}
	}
	if !sameObject(t, filepath.Join(f.path, names[1]), filepath.Join(f.path, names[2])) {
		t.Fatal("pin is not a hardlink of the output")
	}
	if verifyNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original) != nil {
		t.Fatal("staged files do not verify")
	}
}

func TestPrepareNFOCreateFilesRefusesAnExistingTarget(t *testing.T) {
	for _, kind := range []string{"file", "directory", "dangling symlink", "empty file"} {
		t.Run(kind, func(t *testing.T) {
			f := newCreateFixture(t)
			target := filepath.Join(f.path, createTarget)
			switch kind {
			case "file":
				if err := os.WriteFile(target, []byte("<movie><title>mine</title></movie>"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "empty file":
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			case "dangling symlink":
				if err := os.Symlink("missing-target", target); err != nil {
					t.Skip("symlinks unavailable")
				}
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			beforeData, _ := os.ReadFile(target)
			files, err := f.stage(nativeNFOWriteOperations())
			if !errors.Is(err, ErrTargetAppeared) || files != nil || len(f.events) != 0 {
				t.Fatal("a present target was staged against", err, f.events)
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("the foreign target was replaced")
			}
			if afterData, _ := os.ReadFile(target); !bytes.Equal(beforeData, afterData) {
				t.Fatal("the foreign target was modified")
			}
			if names := f.names(); len(names) != 1 || names[0] != createTarget {
				t.Fatal("a refused stage left files behind", names)
			}
		})
	}
}

func TestPrepareNFOCreateFilesRefusesATargetThatAppearsAfterThePlan(t *testing.T) {
	f := newCreateFixture(t)
	foreign := []byte("<movie><title>appeared concurrently</title></movie>")
	persist := f.persistence(0)
	persist.plan = func(context.Context, nfoCommitFilePlan) error {
		f.events = append(f.events, "plan")
		return os.WriteFile(filepath.Join(f.path, createTarget), foreign, 0o600)
	}
	files, err := prepareNFOCreateFiles(context.Background(), f.directory, createTarget, f.replacement, commitFileToken(), persist, nativeNFOWriteOperations())
	if !errors.Is(err, ErrTargetAppeared) || files != nil || !slices.Equal(f.events, []string{"plan"}) {
		t.Fatal("a target that appeared after the plan was staged against", err, f.events)
	}
	if !bytes.Equal(f.readTarget(), foreign) {
		t.Fatal("the foreign target changed")
	}
	if names := f.names(); len(names) != 1 {
		t.Fatal("the stage created files next to a foreign target", names)
	}
}

func TestSettleNFOCreateFilesLinksAfterPhaseOne(t *testing.T) {
	f := newCreateFixture(t)
	files := f.mustStage()
	ops := nativeNFOWriteOperations()
	phasesAtLink := []nfoSettlePhase(nil)
	var phases []nfoSettlePhase
	ops.link = func(r *os.Root, from, to string) error {
		phasesAtLink = slices.Clone(phases)
		return r.Link(from, to)
	}
	progress := func(_ context.Context, phase nfoSettlePhase) error {
		phases = append(phases, phase)
		return nil
	}
	phase, err := settleNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, false, progress, ops)
	if err != nil || phase != nfoSettleReplaced {
		t.Fatal("settle failed", phase, err)
	}
	// Invariant 2: the durable phase 1 precedes the link; phase 2 follows it.
	if !slices.Equal(phasesAtLink, []nfoSettlePhase{nfoSettleBackedUp}) || !slices.Equal(phases, []nfoSettlePhase{nfoSettleBackedUp, nfoSettleReplaced}) {
		t.Fatal("phases do not bracket the link", phasesAtLink, phases)
	}
	if !bytes.Equal(f.readTarget(), f.replacement.original) {
		t.Fatal("target bytes differ")
	}
	names := files.plan.names()
	if !sameObject(t, filepath.Join(f.path, createTarget), filepath.Join(f.path, names[1])) || !sameObject(t, filepath.Join(f.path, createTarget), filepath.Join(f.path, names[2])) {
		t.Fatal("target is not the staged object")
	}
	if current, err := nativeIdentityWithin(f.directory, createTarget); err != nil || current != files.output {
		t.Fatal("target identity differs from the recorded output")
	}
	if verifyNFOCreated(context.Background(), f.directory, *files, f.replacement.original) != nil {
		t.Fatal("linked files do not verify")
	}
	if got := f.names(); len(got) != 3 {
		t.Fatal("create made files beyond target, output and pin", got)
	}
}

// A foreign object at the target is never overwritten, wherever it appears
// between the preparation and the link.
func TestSettleNFOCreateFilesNeverOverwritesAnAppearedTarget(t *testing.T) {
	foreign := []byte("<movie><title>appeared concurrently</title></movie>")
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, target string)
		when  string
	}{
		{"file before settle", func(t *testing.T, target string) {
			if err := os.WriteFile(target, foreign, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "settle"},
		{"file between phase 1 and the link", func(t *testing.T, target string) {
			if err := os.WriteFile(target, foreign, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "phase"},
		{"file inside the link call", func(t *testing.T, target string) {
			if err := os.WriteFile(target, foreign, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "link"},
		{"dangling symlink between phase 1 and the link", func(t *testing.T, target string) {
			if err := os.Symlink("nowhere", target); err != nil {
				t.Skip("symlinks unavailable")
			}
		}, "phase"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newCreateFixture(t)
			files := f.mustStage()
			target := filepath.Join(f.path, createTarget)
			ops := nativeNFOWriteOperations()
			var phases []nfoSettlePhase
			progress := func(_ context.Context, phase nfoSettlePhase) error {
				phases = append(phases, phase)
				if c.when == "phase" && phase == nfoSettleBackedUp {
					c.setup(t, target)
				}
				return nil
			}
			link := ops.link
			ops.link = func(r *os.Root, from, to string) error {
				if c.when == "link" {
					c.setup(t, target)
				}
				return link(r, from, to)
			}
			if c.when == "settle" {
				c.setup(t, target)
			}
			before, err := os.Lstat(target)
			if c.when != "settle" {
				before = nil
				err = nil
			}
			_ = err
			phase, err := settleNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, false, progress, ops)
			if !errors.Is(err, ErrTargetAppeared) || phase == nfoSettleReplaced {
				t.Fatal("the appeared target was not reported", phase, err)
			}
			if slices.Contains(phases, nfoSettleReplaced) {
				t.Fatal("replaced was reported over a foreign target")
			}
			info, err := os.Lstat(target)
			if err != nil {
				t.Fatal("the foreign target vanished", err)
			}
			if before != nil && !os.SameFile(before, info) {
				t.Fatal("the foreign target was replaced")
			}
			if info.Mode().IsRegular() {
				if data, _ := os.ReadFile(target); !bytes.Equal(data, foreign) {
					t.Fatal("the foreign target bytes changed")
				}
			}
			names := files.plan.names()
			if !sameObject(t, filepath.Join(f.path, names[1]), filepath.Join(f.path, names[2])) || info.Mode().IsRegular() && sameObject(t, target, filepath.Join(f.path, names[1])) {
				t.Fatal("the foreign target shares the staged object")
			}
			if c.when == "settle" && len(phases) != 0 {
				t.Fatal("phase 1 was recorded for a target that was already present", phases)
			}
			if c.when != "settle" && !slices.Equal(phases, []nfoSettlePhase{nfoSettleBackedUp}) {
				t.Fatal("phase 1 is expected before the link attempt", phases)
			}
		})
	}
}

func TestSettleNFOCreateFilesResumeStates(t *testing.T) {
	t.Run("linked output completes without another link", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		if _, phase, err := f.settle(files, false, nativeNFOWriteOperations()); err != nil || phase != nfoSettleReplaced {
			t.Fatal(err)
		}
		ops := nativeNFOWriteOperations()
		ops.link = func(*os.Root, string, string) error { t.Fatal("a resumed settle linked again"); return nil }
		phases, phase, err := f.settle(files, true, ops)
		if err != nil || phase != nfoSettleReplaced || !slices.Equal(phases, []nfoSettlePhase{nfoSettleReplaced}) {
			t.Fatal("resume of a linked output failed", phase, err)
		}
	})
	t.Run("absent target after phase 1 continues", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		phases, phase, err := f.settle(files, true, nativeNFOWriteOperations())
		if err != nil || phase != nfoSettleReplaced || !slices.Equal(phases, []nfoSettlePhase{nfoSettleBackedUp, nfoSettleReplaced}) {
			t.Fatal("resume with an absent target failed", phase, err)
		}
	})
	t.Run("output at the target without phase 1 is refused", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		names := files.plan.names()
		if err := f.directory.Link(names[1], createTarget); err != nil {
			t.Fatal(err)
		}
		ops := nativeNFOWriteOperations()
		ops.link = func(*os.Root, string, string) error { t.Fatal("linked without phase 1"); return nil }
		phases, phase, err := f.settle(files, false, ops)
		if !errors.Is(err, ErrChanged) || phase == nfoSettleReplaced || len(phases) != 0 {
			t.Fatal("an unrecorded link was accepted", phase, err, phases)
		}
	})
	t.Run("foreign object after phase 1 is left alone", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		foreign := []byte("<movie><title>foreign</title></movie>")
		if err := os.WriteFile(filepath.Join(f.path, createTarget), foreign, 0o600); err != nil {
			t.Fatal(err)
		}
		ops := nativeNFOWriteOperations()
		ops.link = func(*os.Root, string, string) error { t.Fatal("linked over a foreign object"); return nil }
		phases, phase, err := f.settle(files, true, ops)
		if !errors.Is(err, ErrTargetAppeared) || phase == nfoSettleReplaced || len(phases) != 0 {
			t.Fatal("a foreign object was not reported", phase, err)
		}
		if !bytes.Equal(f.readTarget(), foreign) {
			t.Fatal("the foreign object changed")
		}
	})
	t.Run("output edited in place is not confirmed", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		if _, _, err := f.settle(files, false, nativeNFOWriteOperations()); err != nil {
			t.Fatal(err)
		}
		edit, err := os.OpenFile(filepath.Join(f.path, createTarget), os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := edit.WriteString("<!--edited-->"); err != nil || edit.Close() != nil {
			t.Fatal(err)
		}
		phases, phase, err := f.settle(files, true, nativeNFOWriteOperations())
		if !errors.Is(err, ErrChanged) || phase == nfoSettleReplaced || len(phases) != 0 {
			t.Fatal("an edited object was confirmed", phase, err)
		}
		if !bytes.HasSuffix(f.readTarget(), []byte("<!--edited-->")) {
			t.Fatal("the edited object was touched")
		}
	})
}

func TestSettleNFOCreateFilesUndoesALinkItCannotConfirm(t *testing.T) {
	f := newCreateFixture(t)
	files := f.mustStage()
	ops := nativeNFOWriteOperations()
	syncs := 0
	syncDirectory := ops.syncDirectory
	ops.syncDirectory = func(r *os.Root) error {
		syncs++
		if syncs == 1 {
			return errors.New("directory sync failed")
		}
		return syncDirectory(r)
	}
	phases, phase, err := f.settle(files, false, ops)
	if !errors.Is(err, ErrRolledBack) || phase != nfoSettleRolledBack || slices.Contains(phases, nfoSettleReplaced) {
		t.Fatal("an unconfirmed link was not undone", phase, err)
	}
	if f.targetExists() {
		t.Fatal("the undone link left the target")
	}
	names := files.plan.names()
	if !bytes.Equal(readFileOrFail(t, filepath.Join(f.path, names[1])), f.replacement.original) || !bytes.Equal(readFileOrFail(t, filepath.Join(f.path, names[2])), f.replacement.original) {
		t.Fatal("undoing the link removed token names")
	}
}

func TestSettleNFOCreateFilesLeavesAByteIdenticalObjectSwappedInAfterTheLink(t *testing.T) {
	f := newCreateFixture(t)
	files := f.mustStage()
	ops := nativeNFOWriteOperations()
	syncDirectory := ops.syncDirectory
	swapped := false
	ops.syncDirectory = func(r *os.Root) error {
		if !swapped {
			swapped = true
			// Another object with exactly the replacement bytes takes the name.
			other := filepath.Join(f.path, "swap.tmp")
			if err := os.WriteFile(other, f.replacement.original, 0o600); err != nil {
				return err
			}
			if err := os.Rename(other, filepath.Join(f.path, createTarget)); err != nil {
				return err
			}
			return errors.New("directory sync failed")
		}
		return syncDirectory(r)
	}
	phases, phase, err := f.settle(files, false, ops)
	if !errors.Is(err, ErrChanged) || phase == nfoSettleRolledBack || phase == nfoSettleReplaced || slices.Contains(phases, nfoSettleReplaced) {
		t.Fatal("a swapped object was accepted or undone", phase, err)
	}
	if !f.targetExists() || !bytes.Equal(f.readTarget(), f.replacement.original) || sameObject(t, filepath.Join(f.path, createTarget), filepath.Join(f.path, files.plan.names()[1])) {
		t.Fatal("the swapped object was removed")
	}
}

func readFileOrFail(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAbortNFOCreateFilesRemovesOnlyItsOwnUnchangedOutput(t *testing.T) {
	t.Run("absent target stays absent", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		phase, err := abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, nativeNFOWriteOperations())
		if err != nil || phase != nfoSettleRolledBack || f.targetExists() {
			t.Fatal("abort of an unlinked create failed", phase, err)
		}
	})
	t.Run("linked unchanged output is unlinked", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		if _, _, err := f.settle(files, false, nativeNFOWriteOperations()); err != nil {
			t.Fatal(err)
		}
		phase, err := abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, nativeNFOWriteOperations())
		if err != nil || phase != nfoSettleRolledBack || f.targetExists() {
			t.Fatal("abort did not unlink the linked output", phase, err)
		}
		names := files.plan.names()
		if !bytes.Equal(readFileOrFail(t, filepath.Join(f.path, names[1])), f.replacement.original) {
			t.Fatal("abort removed a token name")
		}
		// Replay is a no-op.
		if phase, err = abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, nativeNFOWriteOperations()); err != nil || phase != nfoSettleRolledBack {
			t.Fatal("abort replay failed", phase, err)
		}
	})
	t.Run("output edited in place is left", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		if _, _, err := f.settle(files, false, nativeNFOWriteOperations()); err != nil {
			t.Fatal(err)
		}
		edit, err := os.OpenFile(filepath.Join(f.path, createTarget), os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := edit.WriteString("<!--user-->"); err != nil || edit.Close() != nil {
			t.Fatal(err)
		}
		phase, err := abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, nativeNFOWriteOperations())
		if !errors.Is(err, ErrChanged) || phase == nfoSettleRolledBack || !f.targetExists() || !bytes.HasSuffix(f.readTarget(), []byte("<!--user-->")) {
			t.Fatal("an edited object was removed or reported rolled back", phase, err)
		}
	})
	t.Run("replaced target is left", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		if _, _, err := f.settle(files, false, nativeNFOWriteOperations()); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(f.path, createTarget)
		replaced := filepath.Join(f.path, "replacement.tmp")
		foreign := []byte("<movie><title>user replaced</title></movie>")
		if err := os.WriteFile(replaced, foreign, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replaced, target); err != nil {
			t.Fatal(err)
		}
		phase, err := abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, nativeNFOWriteOperations())
		if !errors.Is(err, ErrChanged) || phase == nfoSettleRolledBack || !bytes.Equal(f.readTarget(), foreign) {
			t.Fatal("a replaced target was removed or reported rolled back", phase, err)
		}
	})
	t.Run("byte identical replacement object is left", func(t *testing.T) {
		// Only the native identity tells this object from the token's output.
		f := newCreateFixture(t)
		files := f.mustStage()
		if _, _, err := f.settle(files, false, nativeNFOWriteOperations()); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(f.path, createTarget)
		replaced := filepath.Join(f.path, "replacement.tmp")
		if err := os.WriteFile(replaced, f.replacement.original, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replaced, target); err != nil {
			t.Fatal(err)
		}
		if sameObject(t, target, filepath.Join(f.path, files.plan.names()[1])) {
			t.Fatal("the replacement must be another object")
		}
		phase, err := abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, nativeNFOWriteOperations())
		if !errors.Is(err, ErrChanged) || phase == nfoSettleRolledBack || !f.targetExists() || !bytes.Equal(f.readTarget(), f.replacement.original) {
			t.Fatal("a byte identical but different object was removed", phase, err)
		}
	})
	t.Run("foreign object that never was ours is left", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		foreign := []byte("<movie><title>foreign</title></movie>")
		if err := os.WriteFile(filepath.Join(f.path, createTarget), foreign, 0o600); err != nil {
			t.Fatal(err)
		}
		phase, err := abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, nativeNFOWriteOperations())
		if !errors.Is(err, ErrChanged) || phase == nfoSettleRolledBack || !bytes.Equal(f.readTarget(), foreign) {
			t.Fatal("a foreign object was removed", phase, err)
		}
	})
}

// The cleaner removes the token names by identity and never the created
// target, which stays alive through its own name.
func TestCleanNFOCreateKeepsTheCreatedTarget(t *testing.T) {
	f := newCreateFixture(t)
	files := f.mustStage()
	if _, _, err := f.settle(files, false, nativeNFOWriteOperations()); err != nil {
		t.Fatal(err)
	}
	writer := cleanupWriter(t)
	input := domain.NFOWriteCommitCleanupInput{
		Lease:        domain.NFOWriteCommitCleanupLease{Token: cleanupTokenID(commitFileToken())},
		RootPath:     filepath.Dir(f.path),
		RelativePath: filepath.Base(f.path) + "/" + createTarget,
		Plan:         domain.NFOWriteCommitFilePlan{Version: domain.NFOWriteCommitCreatePlanVersion, TargetName: createTarget, ParentIdentity: files.plan.parent.record},
		Namespaces:   []domain.NFOWriteCommitCleanupNamespace{{OutputIdentity: files.output.record}},
	}
	outcome, retained, err := writer.CleanNFOWriteCommitFiles(context.Background(), input)
	if err != nil || outcome != domain.NFOWriteCommitCleanupRemoved || retained != 0 {
		t.Fatal("cleanup of a create failed", outcome, retained, err)
	}
	if got := f.names(); len(got) != 1 || got[0] != createTarget {
		t.Fatal("cleanup left token names or removed the target", got)
	}
	if !bytes.Equal(f.readTarget(), f.replacement.original) {
		t.Fatal("cleanup changed the created target")
	}
	if current, err := nativeIdentityWithin(f.directory, createTarget); err != nil || current != files.output {
		t.Fatal("the created target is not the object cleanup was given")
	}
	// Replay is idempotent.
	if outcome, retained, err = writer.CleanNFOWriteCommitFiles(context.Background(), input); err != nil || outcome != domain.NFOWriteCommitCleanupRemoved || retained != 0 || !bytes.Equal(f.readTarget(), f.replacement.original) {
		t.Fatal("cleanup replay failed", outcome, retained, err)
	}
}

// A name a create never uses is retained and counted, not removed.
func TestCleanNFOCreateRetainsObjectsItDoesNotOwn(t *testing.T) {
	f := newCreateFixture(t)
	files := f.mustStage()
	names := files.plan.names()
	foreign := []byte("not a token object")
	if err := os.WriteFile(filepath.Join(f.path, names[0]), foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	writer := cleanupWriter(t)
	input := domain.NFOWriteCommitCleanupInput{
		Lease:        domain.NFOWriteCommitCleanupLease{Token: cleanupTokenID(commitFileToken())},
		RootPath:     filepath.Dir(f.path),
		RelativePath: filepath.Base(f.path) + "/" + createTarget,
		Plan:         domain.NFOWriteCommitFilePlan{Version: domain.NFOWriteCommitCreatePlanVersion, TargetName: createTarget, ParentIdentity: files.plan.parent.record},
		Namespaces:   []domain.NFOWriteCommitCleanupNamespace{{OutputIdentity: files.output.record}},
	}
	outcome, retained, err := writer.CleanNFOWriteCommitFiles(context.Background(), input)
	if err != nil || outcome != domain.NFOWriteCommitCleanupRetained || retained != 1 {
		t.Fatal("cleanup did not retain the foreign name", outcome, retained, err)
	}
	if !bytes.Equal(readFileOrFail(t, filepath.Join(f.path, names[0])), foreign) {
		t.Fatal("cleanup touched a foreign name")
	}
}
