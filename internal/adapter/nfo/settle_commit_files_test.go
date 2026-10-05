//go:build windows || linux

package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

type settleFixtureState struct {
	path                  string
	directory             *os.Root
	files                 nfoCommitFiles
	original, replacement []byte
}

func settleFixture(t *testing.T, existingBackups int) settleFixtureState {
	t.Helper()
	path, directory, original, replacement := commitFilesFixture(t)
	for i := range existingBackups {
		if err := os.WriteFile(filepath.Join(path, nfoBackupName("movie.nfo", i)), []byte("owned old backup "+strconv.Itoa(i)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(path, "unrelated.txt"), []byte("owned unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := prepareNFOCommitFiles(context.Background(), directory, "movie.nfo", original, replacement, commitFileToken(), nfoCommitFilePersistence{
		plan:  func(context.Context, nfoCommitFilePlan) error { return nil },
		ready: func(context.Context, nfoCommitFiles) error { return nil },
	}, nativeNFOWriteOperations())
	if err != nil {
		t.Fatal("prepare ready commit files", err)
	}
	return settleFixtureState{path, directory, *files, original.original, replacement.original}
}

type settleEntry struct {
	id   nfoNativeIdentity
	data string
}

func settleSnapshot(t *testing.T, s settleFixtureState) map[string]settleEntry {
	t.Helper()
	entries, err := os.ReadDir(s.path)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]settleEntry, len(entries))
	for _, entry := range entries {
		id, err := nativeIdentityWithin(s.directory, entry.Name())
		if err != nil {
			t.Fatal("unexpected non-regular entry", entry.Name())
		}
		var data []byte
		if !entry.IsDir() {
			if data, err = s.directory.ReadFile(entry.Name()); err != nil {
				t.Fatal(err)
			}
		}
		result[entry.Name()] = settleEntry{id, string(data)}
	}
	return result
}

func checkSettleObject(t *testing.T, s settleFixtureState, name string, id nfoNativeIdentity, data []byte) {
	t.Helper()
	current, err := nativeIdentityWithin(s.directory, name)
	if err != nil || current != id {
		t.Fatal("unexpected object at", name)
	}
	if actual, err := s.directory.ReadFile(name); err != nil || !bytes.Equal(actual, data) {
		t.Fatal("unexpected bytes at", name)
	}
}

func recordSettlePhases(phases *[]nfoSettlePhase) nfoSettleProgress {
	return func(_ context.Context, phase nfoSettlePhase) error {
		*phases = append(*phases, phase)
		return nil
	}
}

func TestSettleNFOCommitFilesReplacesAndRotatesBackups(t *testing.T) {
	for _, retained := range []int{0, 1, 3} {
		for _, existing := range []int{0, 4} {
			t.Run(strconv.Itoa(retained)+"_of_"+strconv.Itoa(existing), func(t *testing.T) {
				s := settleFixture(t, existing)
				before := settleSnapshot(t, s)
				names := s.files.plan.names()
				var phases []nfoSettlePhase
				phase, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, retained, recordSettlePhases(&phases), nativeNFOWriteOperations())
				if err != nil || phase != nfoSettleReplaced || !slices.Equal(phases, []nfoSettlePhase{nfoSettleBackedUp, nfoSettleReplaced}) {
					t.Fatal("settlement failed", err, phase, phases)
				}
				checkSettleObject(t, s, "movie.nfo", s.files.output, s.replacement)
				checkSettleObject(t, s, names[0], s.files.plan.target, s.original)
				checkSettleObject(t, s, names[2], s.files.output, s.replacement)
				checkSettleObject(t, s, names[3], s.files.rollback, s.original)
				checkSettleObject(t, s, names[4], s.files.rollback, s.original)
				after := settleSnapshot(t, s)
				evicted := nfoSettleEvictedName(s.files.plan)
				// Simulate expected rotation: shift by one, evict the retained top.
				expected := map[string]settleEntry{}
				if retained > 0 {
					expected[nfoBackupName("movie.nfo", 0)] = settleEntry{s.files.plan.target, string(s.original)}
					for i := 1; i < retained; i++ {
						if old, ok := before[nfoBackupName("movie.nfo", i-1)]; ok {
							expected[nfoBackupName("movie.nfo", i)] = old
						}
					}
					if old, ok := before[nfoBackupName("movie.nfo", retained-1)]; ok {
						expected[evicted] = old
					}
				}
				for i := retained; i < 16; i++ {
					if old, ok := before[nfoBackupName("movie.nfo", i)]; ok {
						expected[nfoBackupName("movie.nfo", i)] = old
					}
				}
				for name, want := range expected {
					if after[name] != want {
						t.Fatal("backup rotation differs at", name)
					}
				}
				if _, ok := expected[evicted]; !ok {
					if _, err := s.directory.Lstat(evicted); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("eviction name created without an evicted backup")
					}
				}
				// No name other than the moved output disappeared.
				for name := range before {
					if _, ok := after[name]; !ok && name != names[1] {
						t.Fatal("settlement removed an object", name)
					}
				}
				if after["unrelated.txt"] != before["unrelated.txt"] {
					t.Fatal("unrelated object changed")
				}
				// Re-invocation completes idempotently without another rename or rotation.
				phases = nil
				phase, err = settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, retained, recordSettlePhases(&phases), nativeNFOWriteOperations())
				if err != nil || phase != nfoSettleReplaced || !slices.Equal(phases, []nfoSettlePhase{nfoSettleReplaced}) {
					t.Fatal("idempotent settlement failed", err, phase, phases)
				}
				againAfter := settleSnapshot(t, s)
				if len(againAfter) != len(after) {
					t.Fatal("re-invocation changed names")
				}
				for name, entry := range after {
					if againAfter[name] != entry {
						t.Fatal("re-invocation changed", name)
					}
				}
			})
		}
	}
}

func TestSettleNFOCommitFilesFailuresBeforeRenameRestoreBackups(t *testing.T) {
	for _, phase := range []string{"evict_rename", "shift_rename", "backup_sync", "backup_progress", "check_source", "cancel_before_rename", "target_rename"} {
		t.Run(phase, func(t *testing.T) {
			s := settleFixture(t, 3)
			before := settleSnapshot(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failed := errors.New("owned injected failure")
			ops := nativeNFOWriteOperations()
			base := ops
			ops.rename = func(root *os.Root, from, to string) error {
				if phase == "evict_rename" && to == nfoSettleEvictedName(s.files.plan) || phase == "shift_rename" && from == nfoBackupName("movie.nfo", 0) && to == nfoBackupName("movie.nfo", 1) || phase == "target_rename" && to == "movie.nfo" {
					return failed
				}
				return base.rename(root, from, to)
			}
			syncs := 0
			ops.syncDirectory = func(root *os.Root) error {
				syncs++
				if phase == "backup_sync" && syncs == 1 {
					return failed
				}
				return base.syncDirectory(root)
			}
			checks := 0
			ops.checkSource = func(context.Context) error {
				checks++
				if phase == "check_source" && checks == 2 {
					return failed
				}
				return nil
			}
			progress := func(_ context.Context, reached nfoSettlePhase) error {
				if reached == nfoSettleReplaced {
					t.Fatal("replaced despite pre-rename failure")
				}
				if phase == "backup_progress" {
					return failed
				}
				if phase == "cancel_before_rename" {
					cancel()
				}
				return nil
			}
			result, err := settleNFOCommitFiles(ctx, s.directory, s.files, s.original, s.replacement, 3, progress, ops)
			if err == nil || result != nfoSettleUnchanged || errors.Is(err, ErrRollback) || errors.Is(err, ErrRolledBack) {
				t.Fatal("pre-rename failure misreported", err, result)
			}
			after := settleSnapshot(t, s)
			if len(after) != len(before) {
				t.Fatal("pre-rename failure left or removed names")
			}
			for name, entry := range before {
				if after[name] != entry {
					t.Fatal("pre-rename failure changed", name)
				}
			}
			// The lock is released and the ready files remain settleable.
			if result, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 3, nil, nativeNFOWriteOperations()); err != nil || result != nfoSettleReplaced {
				t.Fatal("retry after restored failure", err)
			}
		})
	}
}

func TestSettleNFOCommitFilesPartialUndoNeverLosesBackups(t *testing.T) {
	s := settleFixture(t, 3)
	before := settleSnapshot(t, s)
	failed := errors.New("owned injected failure")
	ops := nativeNFOWriteOperations()
	base := ops
	ops.rename = func(root *os.Root, from, to string) error {
		// Fail the target rename and the reversal of the oldest shift.
		if to == "movie.nfo" || from == nfoBackupName("movie.nfo", 2) && to == nfoBackupName("movie.nfo", 1) {
			return failed
		}
		return base.rename(root, from, to)
	}
	result, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 3, nil, ops)
	if !errors.Is(err, ErrReplace) || result != nfoSettleUnchanged {
		t.Fatal("partial undo misreported", err, result)
	}
	after := settleSnapshot(t, s)
	if after["movie.nfo"] != before["movie.nfo"] {
		t.Fatal("target changed")
	}
	for i := range 3 {
		old := before[nfoBackupName("movie.nfo", i)]
		found := false
		for _, entry := range after {
			found = found || entry == old
		}
		if !found {
			t.Fatal("backup lost after partial undo", i)
		}
	}
	// An unfinished rotation is refused rather than guessed.
	if _, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 3, nil, nativeNFOWriteOperations()); !errors.Is(err, ErrChanged) {
		t.Fatal("unfinished rotation was overwritten", err)
	}
}

func TestSettleNFOCommitFilesFailuresAfterRenameRollBack(t *testing.T) {
	for _, phase := range []string{"target_sync", "rename_reported_failure", "rollback_rename", "rollback_sync", "foreign_target"} {
		t.Run(phase, func(t *testing.T) {
			s := settleFixture(t, 1)
			names := s.files.plan.names()
			failed := errors.New("owned injected failure")
			ops := nativeNFOWriteOperations()
			base := ops
			targetRenames := 0
			ops.rename = func(root *os.Root, from, to string) error {
				if to != "movie.nfo" {
					return base.rename(root, from, to)
				}
				targetRenames++
				if targetRenames == 1 && phase == "rename_reported_failure" {
					if err := base.rename(root, from, to); err != nil {
						t.Fatal(err)
					}
					return failed
				}
				if targetRenames == 2 && phase == "rollback_rename" {
					return failed
				}
				if targetRenames == 1 && phase == "foreign_target" {
					if err := base.rename(root, from, to); err != nil {
						t.Fatal(err)
					}
					if err := root.WriteFile("foreign.nfo", s.original, 0600); err != nil {
						t.Fatal(err)
					}
					return base.rename(root, "foreign.nfo", to)
				}
				return base.rename(root, from, to)
			}
			syncs := 0
			ops.syncDirectory = func(root *os.Root) error {
				syncs++
				// Sync 1 is the backup rotation; sync 2 follows the target rename.
				if syncs == 2 && phase != "rename_reported_failure" || syncs == 3 && phase == "rollback_sync" {
					return failed
				}
				return base.syncDirectory(root)
			}
			var phases []nfoSettlePhase
			result, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 1, recordSettlePhases(&phases), ops)
			if slices.Contains(phases, nfoSettleReplaced) {
				t.Fatal("failed replacement was checkpointed")
			}
			for _, witness := range []string{names[0], names[2], names[4]} {
				if _, err := s.directory.Lstat(witness); err != nil {
					t.Fatal("rollback removed a witness", witness)
				}
			}
			if phase == "foreign_target" {
				// A foreign target is never replaced by the rollback object.
				if !errors.Is(err, ErrRollback) || result != nfoSettleIndeterminate {
					t.Fatal("foreign target not reported", err, result)
				}
				checkSettleObject(t, s, names[3], s.files.rollback, s.original)
				checkSettleObject(t, s, names[2], s.files.output, s.replacement)
				return
			}
			if phase == "rollback_rename" || phase == "rollback_sync" {
				if !errors.Is(err, ErrRollback) || errors.Is(err, ErrRolledBack) || result != nfoSettleIndeterminate {
					t.Fatal("rollback failure not distinguished", err, result)
				}
				if phase == "rollback_rename" {
					checkSettleObject(t, s, "movie.nfo", s.files.output, s.replacement)
					checkSettleObject(t, s, names[3], s.files.rollback, s.original)
				}
				return
			}
			if !errors.Is(err, ErrRolledBack) || errors.Is(err, ErrRollback) || result != nfoSettleRolledBack {
				t.Fatal("rollback not reported", err, result)
			}
			checkSettleObject(t, s, "movie.nfo", s.files.rollback, s.original)
			checkSettleObject(t, s, names[0], s.files.plan.target, s.original)
			checkSettleObject(t, s, names[2], s.files.output, s.replacement)
			// A concluded rollback is reported again without another rename.
			snapshot := settleSnapshot(t, s)
			result, err = settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 1, nil, nativeNFOWriteOperations())
			if !errors.Is(err, ErrRolledBack) || result != nfoSettleRolledBack {
				t.Fatal("rolled back resume", err, result)
			}
			for name, entry := range settleSnapshot(t, s) {
				if snapshot[name] != entry {
					t.Fatal("rolled back resume changed", name)
				}
			}
		})
	}
}

func TestSettleNFOCommitFilesRejectsExternalChanges(t *testing.T) {
	for _, change := range []string{"target_in_place", "target_same_bytes", "output_witness_same_bytes", "rollback_missing", "original_pin_missing", "backup_directory"} {
		t.Run(change, func(t *testing.T) {
			s := settleFixture(t, 1)
			names := s.files.plan.names()
			var err error
			switch change {
			case "target_in_place":
				err = s.directory.WriteFile("movie.nfo", []byte("<movie><title>user edit</title></movie>"), 0600)
			case "target_same_bytes":
				if err = s.directory.Rename("movie.nfo", "moved.nfo"); err == nil {
					err = s.directory.WriteFile("movie.nfo", s.original, 0600)
				}
			case "output_witness_same_bytes":
				if err = s.directory.Rename(names[2], "moved-output"); err == nil {
					err = s.directory.WriteFile(names[2], s.replacement, 0600)
				}
			case "rollback_missing":
				err = s.directory.Rename(names[3], "moved-rollback")
			case "original_pin_missing":
				err = s.directory.Rename(names[0], "moved-pin")
			case "backup_directory":
				if err = s.directory.Remove(nfoBackupName("movie.nfo", 0)); err == nil {
					err = s.directory.Mkdir(nfoBackupName("movie.nfo", 0), 0700)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before := settleSnapshot(t, s)
			result, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 1, func(context.Context, nfoSettlePhase) error {
				t.Fatal("refused settlement reported progress")
				return nil
			}, nativeNFOWriteOperations())
			want := ErrChanged
			if change == "backup_directory" {
				want = ErrReplace
			}
			if !errors.Is(err, want) || result != nfoSettleUnchanged {
				t.Fatal("external change accepted", err, result)
			}
			after := settleSnapshot(t, s)
			if len(after) != len(before) {
				t.Fatal("refusal changed names")
			}
			for name, entry := range before {
				if after[name] != entry {
					t.Fatal("refusal changed", name)
				}
			}
		})
	}
}

func TestSettleNFOCommitFilesResumesCompletedPhases(t *testing.T) {
	t.Run("backup_completed", func(t *testing.T) {
		s := settleFixture(t, 2)
		lock, err := lockNFOFile(context.Background(), s.directory, "movie.nfo")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rotateNFOSettleBackups(context.Background(), s.directory, s.files, 3, nativeNFOWriteOperations(), lock); err != nil {
			t.Fatal(err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		rotated := settleSnapshot(t, s)
		var phases []nfoSettlePhase
		result, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 3, recordSettlePhases(&phases), nativeNFOWriteOperations())
		if err != nil || result != nfoSettleReplaced || !slices.Equal(phases, []nfoSettlePhase{nfoSettleBackedUp, nfoSettleReplaced}) {
			t.Fatal("resume after backup", err, result, phases)
		}
		for i := range 3 {
			name := nfoBackupName("movie.nfo", i)
			if settleSnapshot(t, s)[name] != rotated[name] {
				t.Fatal("completed backup phase rotated twice", i)
			}
		}
	})
	t.Run("replaced_checkpoint_failed", func(t *testing.T) {
		s := settleFixture(t, 0)
		result, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 2, func(_ context.Context, phase nfoSettlePhase) error {
			if phase == nfoSettleReplaced {
				return errors.New("owned injected ambiguous commit outcome")
			}
			return nil
		}, nativeNFOWriteOperations())
		if !errors.Is(err, ErrReplace) || result != nfoSettleReplaced {
			t.Fatal("checkpoint failure hid the replaced state", err, result)
		}
		checkSettleObject(t, s, "movie.nfo", s.files.output, s.replacement)
		if result, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 2, nil, nativeNFOWriteOperations()); err != nil || result != nfoSettleReplaced {
			t.Fatal("replaced resume", err, result)
		}
		checkSettleObject(t, s, "movie.nfo", s.files.output, s.replacement)
	})
	t.Run("replaced_state_tampered", func(t *testing.T) {
		s := settleFixture(t, 0)
		if _, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 0, nil, nativeNFOWriteOperations()); err != nil {
			t.Fatal(err)
		}
		if err := s.directory.WriteFile(s.files.plan.names()[1], s.replacement, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 0, nil, nativeNFOWriteOperations()); !errors.Is(err, ErrChanged) {
			t.Fatal("foreign output name accepted on resume", err)
		}
	})
}

func TestSettleNFOCommitFilesRejectsInvalidInput(t *testing.T) {
	s := settleFixture(t, 0)
	noRollback := s.files
	noRollback.rollback = nfoNativeIdentity{}
	noRename := nativeNFOWriteOperations()
	noRename.rename = nil
	for _, call := range []func() (nfoSettlePhase, error){
		func() (nfoSettlePhase, error) {
			return settleNFOCommitFiles(context.Background(), s.directory, noRollback, s.original, s.replacement, 1, nil, nativeNFOWriteOperations())
		},
		func() (nfoSettlePhase, error) {
			return settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 17, nil, nativeNFOWriteOperations())
		},
		func() (nfoSettlePhase, error) {
			return settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 1, nil, noRename)
		},
		func() (nfoSettlePhase, error) {
			return settleNFOCommitFiles(context.Background(), s.directory, s.files, nil, s.replacement, 1, nil, nativeNFOWriteOperations())
		},
	} {
		if _, err := call(); !errors.Is(err, ErrInvalidInput) {
			t.Fatal("invalid settlement input accepted", err)
		}
	}
	checkSettleObject(t, s, "movie.nfo", s.files.plan.target, s.original)
}
