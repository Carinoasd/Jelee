//go:build windows || linux

package nfo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

func cleanupWriter(t *testing.T) *Writer {
	t.Helper()
	budget, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 2, Queue: 4})
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriterWithBudget(budget)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func cleanupTokenID(token [16]byte) string {
	h := hex.EncodeToString(token[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// cleanupInput mirrors what PostgreSQL records for a token whose only
// namespace is files' attempt. The fixture directory sits below a library
// root so the path components are walked.
func cleanupInput(s settleFixtureState, files nfoCommitFiles) domain.NFOWriteCommitCleanupInput {
	return domain.NFOWriteCommitCleanupInput{
		Lease:        domain.NFOWriteCommitCleanupLease{Token: cleanupTokenID(files.plan.token), Owner: "cleaner", Epoch: 1},
		RootPath:     filepath.Dir(s.path),
		RelativePath: filepath.Base(s.path) + "/" + files.plan.filename,
		Plan:         domain.NFOWriteCommitFilePlan{Version: 1, TargetName: files.plan.filename, ParentIdentity: files.plan.parent.record, TargetIdentity: files.plan.target.record},
		Namespaces:   []domain.NFOWriteCommitCleanupNamespace{{Attempt: files.plan.attempt, OutputIdentity: files.output.record, RollbackIdentity: files.rollback.record}},
	}
}

func checkCleanupResult(t *testing.T, outcome domain.NFOWriteCommitCleanupOutcome, retained int, err error, want domain.NFOWriteCommitCleanupOutcome, wantRetained int) {
	t.Helper()
	if err != nil || outcome != want || retained != wantRetained {
		t.Fatal("unexpected cleanup result", outcome, retained, err)
	}
}

func checkAbsent(t *testing.T, s settleFixtureState, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := s.directory.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("retained object not removed", name)
		}
	}
}

func settleReplaced(t *testing.T, s settleFixtureState, backups int) {
	t.Helper()
	phase, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, backups, nil, nativeNFOWriteOperations())
	if err != nil || phase != nfoSettleReplaced {
		t.Fatal("settlement failed", phase, err)
	}
}

func TestCleanNFOCommitFilesRemovesReplacedTokenObjects(t *testing.T) {
	s := settleFixture(t, 0)
	settleReplaced(t, s, 1)
	w := cleanupWriter(t)
	input := cleanupInput(s, s.files)
	outcome, retained, err := w.CleanNFOWriteCommitFiles(context.Background(), input)
	checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupRemoved, 0)
	names := s.files.plan.names()
	checkAbsent(t, s, names[:]...)
	// The target and the retained backup are not the token's to remove.
	checkSettleObject(t, s, "movie.nfo", s.files.output, s.replacement)
	checkSettleObject(t, s, nfoBackupName("movie.nfo", 0), s.files.plan.target, s.original)
	if data, err := s.directory.ReadFile("unrelated.txt"); err != nil || string(data) != "owned unrelated" {
		t.Fatal("unrelated object changed")
	}
	before := settleSnapshot(t, s)
	outcome, retained, err = w.CleanNFOWriteCommitFiles(context.Background(), input)
	checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupRemoved, 0)
	after := settleSnapshot(t, s)
	if len(after) != len(before) {
		t.Fatal("repeated cleanup changed names")
	}
	for name, entry := range before {
		if after[name] != entry {
			t.Fatal("repeated cleanup changed", name)
		}
	}
}

func TestCleanNFOCommitFilesRemovesUnsettledAndRolledBackObjects(t *testing.T) {
	t.Run("never_renamed", func(t *testing.T) {
		s := settleFixture(t, 0)
		outcome, retained, err := cleanupWriter(t).CleanNFOWriteCommitFiles(context.Background(), cleanupInput(s, s.files))
		checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupRemoved, 0)
		names := s.files.plan.names()
		checkAbsent(t, s, names[:]...)
		// Removing the original pin only drops a link: the target keeps it.
		checkSettleObject(t, s, "movie.nfo", s.files.plan.target, s.original)
	})
	t.Run("rolled_back", func(t *testing.T) {
		s := settleFixture(t, 0)
		ops, calls := nativeNFOWriteOperations(), 0
		ops.syncDirectory = func(r *os.Root) error {
			calls++
			if calls == 1 {
				return errors.New("owned directory sync failure")
			}
			return syncNFODirectory(r)
		}
		phase, err := settleNFOCommitFiles(context.Background(), s.directory, s.files, s.original, s.replacement, 0, nil, ops)
		if phase != nfoSettleRolledBack || !errors.Is(err, ErrRolledBack) {
			t.Fatal("rollback fixture failed", phase, err)
		}
		outcome, retained, err := cleanupWriter(t).CleanNFOWriteCommitFiles(context.Background(), cleanupInput(s, s.files))
		checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupRemoved, 0)
		names := s.files.plan.names()
		checkAbsent(t, s, names[:]...)
		checkSettleObject(t, s, "movie.nfo", s.files.rollback, s.original)
	})
}

func TestCleanNFOCommitFilesKeepsObjectsWithoutMatchingIdentity(t *testing.T) {
	t.Run("replaced_pin", func(t *testing.T) {
		s := settleFixture(t, 0)
		settleReplaced(t, s, 1)
		names := s.files.plan.names()
		if err := s.directory.Remove(names[2]); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.path, names[2]), []byte("someone else's object"), 0600); err != nil {
			t.Fatal(err)
		}
		outcome, retained, err := cleanupWriter(t).CleanNFOWriteCommitFiles(context.Background(), cleanupInput(s, s.files))
		checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupRetained, 1)
		if data, err := s.directory.ReadFile(names[2]); err != nil || string(data) != "someone else's object" {
			t.Fatal("foreign object removed")
		}
		checkAbsent(t, s, names[0], names[1], names[3], names[4])
	})
	t.Run("wrong_record", func(t *testing.T) {
		// Reverse check: the output pin is ours, but the durable record names
		// another object. Without the identity comparison it would be removed.
		s := settleFixture(t, 0)
		settleReplaced(t, s, 1)
		input := cleanupInput(s, s.files)
		input.Namespaces[0].OutputIdentity = s.files.rollback.record
		input.Namespaces[0].RollbackIdentity = s.files.output.record
		outcome, retained, err := cleanupWriter(t).CleanNFOWriteCommitFiles(context.Background(), input)
		checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupRetained, 3)
		names := s.files.plan.names()
		checkSettleObject(t, s, names[2], s.files.output, s.replacement)
		checkSettleObject(t, s, names[3], s.files.rollback, s.original)
		checkSettleObject(t, s, names[4], s.files.rollback, s.original)
		checkAbsent(t, s, names[0])
	})
	t.Run("unrecorded_identity", func(t *testing.T) {
		// A rollback stage created before its checkpoint has no record.
		s := settleFixture(t, 0)
		input := cleanupInput(s, s.files)
		input.Namespaces[0].RollbackIdentity = [48]byte{}
		outcome, retained, err := cleanupWriter(t).CleanNFOWriteCommitFiles(context.Background(), input)
		checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupRetained, 2)
		names := s.files.plan.names()
		checkSettleObject(t, s, names[3], s.files.rollback, s.original)
		checkAbsent(t, s, names[0], names[1], names[2])
	})
	t.Run("directory", func(t *testing.T) {
		s := settleFixture(t, 0)
		names := s.files.plan.names()
		if err := s.directory.Remove(names[4]); err != nil {
			t.Fatal(err)
		}
		if err := s.directory.Mkdir(names[4], 0700); err != nil {
			t.Fatal(err)
		}
		outcome, retained, err := cleanupWriter(t).CleanNFOWriteCommitFiles(context.Background(), cleanupInput(s, s.files))
		checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupRetained, 1)
		if info, err := s.directory.Lstat(names[4]); err != nil || !info.IsDir() {
			t.Fatal("foreign directory removed")
		}
	})
}

func TestCleanNFOCommitFilesEvictedBackupNeedsEarlierOriginal(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(strconv.FormatBool(known), func(t *testing.T) {
			first := settleFixture(t, 0)
			settleReplaced(t, first, 1)
			// A second token for the same file evicts the first original.
			original, replacement := editDocument(t, first.replacement), editDocument(t, first.replacement)
			replacement, err := replacement.WithText(context.Background(), 0, "title", "newer", DefaultMaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			token := commitFileToken()
			token[15] = 99
			files, err := prepareNFOCommitFiles(context.Background(), first.directory, "movie.nfo", original, replacement, token, nfoCommitFilePersistence{
				plan:  func(context.Context, nfoCommitFilePlan) error { return nil },
				ready: func(context.Context, nfoCommitFiles) error { return nil },
			}, nativeNFOWriteOperations())
			if err != nil {
				t.Fatal("prepare second token", err)
			}
			second := settleFixtureState{first.path, first.directory, *files, original.original, replacement.original}
			settleReplaced(t, second, 1)
			evicted := nfoSettleEvictedName(second.files.plan)
			checkSettleObject(t, second, evicted, first.files.plan.target, first.original)
			input := cleanupInput(second, second.files)
			if known {
				input.EvictedIdentities = [][48]byte{first.files.plan.target.record}
			}
			outcome, retained, err := cleanupWriter(t).CleanNFOWriteCommitFiles(context.Background(), input)
			if known {
				checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupRemoved, 0)
				checkAbsent(t, second, evicted)
			} else {
				checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupRetained, 1)
				checkSettleObject(t, second, evicted, first.files.plan.target, first.original)
			}
			checkSettleObject(t, second, nfoBackupName("movie.nfo", 0), second.files.plan.target, second.original)
			checkSettleObject(t, second, "movie.nfo", second.files.output, second.replacement)
		})
	}
}

// The planned parent is looked up by path. Pointing the path elsewhere stands
// in for a removed or replaced directory: Windows keeps a removed directory
// that still has open handles delete-pending, so the fixture is not removed.
func TestCleanNFOCommitFilesParentChanges(t *testing.T) {
	for name, path := range map[string]func(t *testing.T, s settleFixtureState) string{
		"removed": func(*testing.T, settleFixtureState) string { return "absent" },
		"replaced": func(t *testing.T, s settleFixtureState) string {
			other := filepath.Base(s.path) + "-other"
			if err := os.Mkdir(filepath.Join(filepath.Dir(s.path), other), 0700); err != nil {
				t.Fatal(err)
			}
			return other
		},
		"not_directory": func(t *testing.T, s settleFixtureState) string {
			file := filepath.Base(s.path) + "-file"
			if err := os.WriteFile(filepath.Join(filepath.Dir(s.path), file), []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
			return file
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := settleFixture(t, 0)
			input := cleanupInput(s, s.files)
			input.RelativePath = path(t, s) + "/movie.nfo"
			outcome, retained, err := cleanupWriter(t).CleanNFOWriteCommitFiles(context.Background(), input)
			checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupParentGone, 0)
			// The planned directory keeps every object; nothing was removed.
			for _, name := range s.files.plan.names() {
				if _, err := s.directory.Lstat(name); err != nil {
					t.Fatal("object removed from the planned directory", name)
				}
			}
		})
	}
	t.Run("root_missing", func(t *testing.T) {
		s := settleFixture(t, 0)
		input := cleanupInput(s, s.files)
		input.RootPath = filepath.Join(input.RootPath, "absent-library")
		if _, _, err := cleanupWriter(t).CleanNFOWriteCommitFiles(context.Background(), input); !errors.Is(err, ErrChanged) {
			t.Fatal("missing library root concluded", err)
		}
	})
}

func TestCleanNFOCommitFilesRejectsInvalidInput(t *testing.T) {
	s := settleFixture(t, 0)
	w := cleanupWriter(t)
	for name, change := range map[string]func(*domain.NFOWriteCommitCleanupInput){
		"token":     func(i *domain.NFOWriteCommitCleanupInput) { i.Lease.Token = "x" },
		"name":      func(i *domain.NFOWriteCommitCleanupInput) { i.Plan.TargetName = "movie.txt" },
		"relative":  func(i *domain.NFOWriteCommitCleanupInput) { i.RelativePath = "../" + i.RelativePath },
		"mismatch":  func(i *domain.NFOWriteCommitCleanupInput) { i.RelativePath = filepath.Base(s.path) + "/other.nfo" },
		"absolute":  func(i *domain.NFOWriteCommitCleanupInput) { i.RootPath = "relative" },
		"empty":     func(i *domain.NFOWriteCommitCleanupInput) { i.Namespaces = nil },
		"attempt":   func(i *domain.NFOWriteCommitCleanupInput) { i.Namespaces[0].Attempt = 4 },
		"duplicate": func(i *domain.NFOWriteCommitCleanupInput) { i.Namespaces = append(i.Namespaces, i.Namespaces[0]) },
		"target":    func(i *domain.NFOWriteCommitCleanupInput) { i.Namespaces[0].OutputIdentity = i.Plan.TargetIdentity },
		"parent":    func(i *domain.NFOWriteCommitCleanupInput) { i.Plan.ParentIdentity = i.Plan.TargetIdentity },
		"evicted":   func(i *domain.NFOWriteCommitCleanupInput) { i.EvictedIdentities = [][48]byte{{}} },
	} {
		t.Run(name, func(t *testing.T) {
			input := cleanupInput(s, s.files)
			change(&input)
			if _, _, err := w.CleanNFOWriteCommitFiles(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
				t.Fatal("invalid input accepted", err)
			}
		})
	}
	names := s.files.plan.names()
	for _, name := range names {
		if _, err := s.directory.Lstat(name); err != nil {
			t.Fatal("invalid input removed an object", name)
		}
	}
}

// cleanupProcessInput carries the durable identities to the helper process.
type cleanupProcessInput struct {
	RootPath, RelativePath, Token    string
	Parent, Target, Output, Rollback []byte
}

// An abrupt exit just before or just after each removal must leave a state the
// next pass completes, and must never remove anything else.
func TestCleanNFOCommitFilesInterruptedProcess(t *testing.T) {
	for removal := 1; removal <= 4; removal++ {
		for _, point := range []string{"before", "after"} {
			t.Run(point+"_"+strconv.Itoa(removal), func(t *testing.T) {
				s := settleFixture(t, 0)
				settleReplaced(t, s, 1)
				input := cleanupInput(s, s.files)
				encoded, err := json.Marshal(cleanupProcessInput{RootPath: input.RootPath, RelativePath: input.RelativePath, Token: input.Lease.Token,
					Parent: input.Plan.ParentIdentity[:], Target: input.Plan.TargetIdentity[:], Output: input.Namespaces[0].OutputIdentity[:], Rollback: input.Namespaces[0].RollbackIdentity[:]})
				if err != nil {
					t.Fatal(err)
				}
				inputPath := filepath.Join(t.TempDir(), "cleanup.json")
				if err := os.WriteFile(inputPath, encoded, 0600); err != nil {
					t.Fatal(err)
				}
				before := settleSnapshot(t, s)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCleanNFOCommitFilesInterruptedHelper$", "-test.count=1")
				cmd.Env = append(os.Environ(), "JELEE_OWNED_CLEANUP_INPUT="+inputPath, "JELEE_OWNED_CLEANUP_POINT="+point, "JELEE_OWNED_CLEANUP_REMOVAL="+strconv.Itoa(removal))
				output, err := cmd.CombinedOutput()
				_ = output // Never print private child diagnostics.
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != 80 {
					t.Fatal("helper did not stop at the selected removal", err)
				}
				pins := map[string]bool{}
				for _, name := range s.files.plan.names() {
					pins[name] = true
				}
				interrupted := settleSnapshot(t, s)
				removed := 0
				for name, entry := range before {
					current, ok := interrupted[name]
					if !ok {
						if !pins[name] {
							t.Fatal("interrupted cleanup removed a foreign object", name)
						}
						removed++
						continue
					}
					if current != entry {
						t.Fatal("interrupted cleanup changed", name)
					}
				}
				want := removal - 1
				if point == "after" {
					want = removal
				}
				if removed != want {
					t.Fatal("unexpected removals before exit", removed, want)
				}
				outcome, retained, err := cleanupWriter(t).CleanNFOWriteCommitFiles(context.Background(), input)
				checkCleanupResult(t, outcome, retained, err, domain.NFOWriteCommitCleanupRemoved, 0)
				names := s.files.plan.names()
				checkAbsent(t, s, names[:]...)
				checkSettleObject(t, s, "movie.nfo", s.files.output, s.replacement)
				checkSettleObject(t, s, nfoBackupName("movie.nfo", 0), s.files.plan.target, s.original)
			})
		}
	}
}

func TestCleanNFOCommitFilesInterruptedHelper(t *testing.T) {
	inputPath := os.Getenv("JELEE_OWNED_CLEANUP_INPUT")
	if inputPath == "" {
		return
	}
	encoded, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal("owned fixture operation failed")
	}
	var recipe cleanupProcessInput
	if err := json.Unmarshal(encoded, &recipe); err != nil {
		t.Fatal(err)
	}
	input := domain.NFOWriteCommitCleanupInput{
		Lease:        domain.NFOWriteCommitCleanupLease{Token: recipe.Token, Owner: "cleaner", Epoch: 1},
		RootPath:     recipe.RootPath,
		RelativePath: recipe.RelativePath,
		Plan:         domain.NFOWriteCommitFilePlan{Version: 1, TargetName: "movie.nfo", ParentIdentity: [48]byte(recipe.Parent), TargetIdentity: [48]byte(recipe.Target)},
		Namespaces:   []domain.NFOWriteCommitCleanupNamespace{{OutputIdentity: [48]byte(recipe.Output), RollbackIdentity: [48]byte(recipe.Rollback)}},
	}
	point := os.Getenv("JELEE_OWNED_CLEANUP_POINT")
	selected, err := strconv.Atoi(os.Getenv("JELEE_OWNED_CLEANUP_REMOVAL"))
	if err != nil {
		t.Fatal(err)
	}
	ops, count := nativeNFOCleanupOperations(), 0
	remove := ops.remove
	ops.remove = func(r *os.Root, name string) error {
		count++
		if count == selected && point == "before" {
			os.Exit(80)
		}
		if err := remove(r, name); err != nil {
			return err
		}
		if count == selected && point == "after" {
			os.Exit(80)
		}
		return nil
	}
	_, _, err = cleanupWriter(t).cleanCommitFiles(context.Background(), input, ops)
	t.Fatal("helper finished without reaching the selected removal", err)
}
