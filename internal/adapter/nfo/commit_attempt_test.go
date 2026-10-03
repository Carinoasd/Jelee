//go:build windows || linux

package nfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type ownedAttemptRecipe struct{ Original, Replacement []byte }
type ownedAttemptReservation struct {
	Version                                        uint8
	Token                                          [16]byte
	Filename                                       string
	Parent, Target                                 [48]byte
	Attempt                                        uint8
	OriginalBytes, ReplacementBytes, RetainedBytes int64
	OriginalHash, ReplacementHash                  [32]byte
}

func ownedAttemptRecord(v nfoCommitAttemptReservation) ownedAttemptReservation {
	return ownedAttemptReservation{v.plan.version, v.plan.token, v.plan.filename, v.plan.parent.record, v.plan.target.record, v.plan.attempt, v.originalBytes, v.replacementBytes, v.retainedBytes, v.originalHash, v.replacementHash}
}
func (v ownedAttemptReservation) plan() nfoCommitFilePlan {
	return nfoCommitFilePlan{version: v.Version, token: v.Token, filename: v.Filename, parent: nfoNativeIdentity{v.Parent}, target: nfoNativeIdentity{v.Target}, attempt: v.Attempt}
}
func saveOwnedAttempt(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func TestNFOCommitAttemptAbruptNextNamespace(t *testing.T) {
	for _, phase := range []string{"output_sync", "rollback_sync", "output_directory", "complete_directory"} {
		t.Run(phase, func(t *testing.T) {
			path, root, original, replacement := commitFilesFixture(t)
			recipePath, reservationPath := filepath.Join(t.TempDir(), "recipe.json"), filepath.Join(t.TempDir(), "reservation.json")
			if saveOwnedAttempt(recipePath, ownedAttemptRecipe{original.original, replacement.original}) != nil {
				t.Fatal("owned recipe save failed")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNFOCommitAttemptAbruptHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "JELEE_OWNED_ATTEMPT_ROOT="+path, "JELEE_OWNED_ATTEMPT_RECIPE="+recipePath, "JELEE_OWNED_ATTEMPT_RESERVATION="+reservationPath, "JELEE_OWNED_ATTEMPT_PHASE="+phase)
			_, err := cmd.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 101 {
				t.Fatal("child missed owned interruption boundary")
			}
			data, err := os.ReadFile(reservationPath)
			if err != nil {
				t.Fatal("first reservation missing")
			}
			var first ownedAttemptReservation
			if json.Unmarshal(data, &first) != nil {
				t.Fatal("first reservation decode failed")
			}
			expectedBytes := int64(maxNFOCommitAttempts+1) * (3*int64(len(original.original)) + 2*int64(len(replacement.original)))
			if first.Attempt != 1 || first.RetainedBytes != expectedBytes || first.OriginalHash != sha256.Sum256(original.original) || first.ReplacementHash != sha256.Sum256(replacement.original) {
				t.Fatal("first reservation scope differs")
			}
			firstNames := first.plan().names()
			before := ownedProgressArtifacts(t, root, firstNames)
			fresh, err := os.OpenRoot(path)
			if err != nil {
				t.Fatal("fresh root unavailable")
			}
			defer fresh.Close()
			var second ownedAttemptReservation
			ready := false
			files, err := prepareNFOCommitAttempt(context.Background(), fresh, "movie.nfo", original, replacement, commitFileToken(), 2, nfoCommitAttemptPersistence{
				reserve: func(_ context.Context, v nfoCommitAttemptReservation) (nfoCommitAttemptReservation, error) {
					second = ownedAttemptRecord(v)
					if v.plan.attempt != 2 || v.retainedBytes != first.RetainedBytes || v.plan.parent.record != first.Parent || v.plan.target.record != first.Target || ownedProgressArtifacts(t, root, firstNames) != before {
						t.Fatal("second reservation changed earlier evidence")
					}
					for _, n := range v.plan.names() {
						if _, e := fresh.Lstat(n); !errors.Is(e, os.ErrNotExist) {
							t.Fatal("side effect before second reservation")
						}
					}
					if saveOwnedAttempt(filepath.Join(t.TempDir(), "second.json"), second) != nil {
						t.Fatal("second reservation save failed")
					}
					return v, nil
				},
				progress: func(context.Context, nfoCommitFiles) error { return nil },
				ready:    func(_ context.Context, v nfoCommitFiles) error { ready = true; return nil },
			}, nativeNFOWriteOperations())
			if err != nil || files == nil || !ready || files.plan.attempt != 2 || verifyNFOCommitFiles(context.Background(), fresh, *files, original.original, replacement.original) != nil {
				t.Fatal("second namespace did not reach verified ready")
			}
			if ownedProgressArtifacts(t, root, firstNames) != before {
				t.Fatal("retry changed unproven earlier artifacts")
			}
			for _, a := range firstNames {
				for _, b := range second.plan().names() {
					if a == b {
						t.Fatal("attempt namespaces overlap")
					}
				}
			}
			if actual, e := fresh.ReadFile("movie.nfo"); e != nil || !bytes.Equal(actual, original.original) {
				t.Fatal("retry changed original target")
			}
		})
	}
}

func TestNFOCommitAttemptAbruptHelper(t *testing.T) {
	path := os.Getenv("JELEE_OWNED_ATTEMPT_ROOT")
	if path == "" {
		return
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal("owned root unavailable")
	}
	defer root.Close()
	data, err := os.ReadFile(os.Getenv("JELEE_OWNED_ATTEMPT_RECIPE"))
	if err != nil {
		t.Fatal("owned recipe unavailable")
	}
	var recipe ownedAttemptRecipe
	if json.Unmarshal(data, &recipe) != nil {
		t.Fatal("owned recipe decode failed")
	}
	original, replacement := editDocument(t, recipe.Original), editDocument(t, recipe.Replacement)
	phase := os.Getenv("JELEE_OWNED_ATTEMPT_PHASE")
	ops := nativeNFOWriteOperations()
	syncFile, syncDirectory := ops.syncFile, ops.syncDirectory
	fileCount, directoryCount := 0, 0
	ops.syncFile = func(f *os.File) error {
		if e := syncFile(f); e != nil {
			return e
		}
		fileCount++
		if phase == "output_sync" && fileCount == 1 || phase == "rollback_sync" && fileCount == 2 {
			os.Exit(101)
		}
		return nil
	}
	ops.syncDirectory = func(r *os.Root) error {
		if e := syncDirectory(r); e != nil {
			return e
		}
		directoryCount++
		if phase == "output_directory" && directoryCount == 1 || phase == "complete_directory" && directoryCount == 2 {
			os.Exit(101)
		}
		return nil
	}
	_, _ = prepareNFOCommitAttempt(context.Background(), root, "movie.nfo", original, replacement, commitFileToken(), 1, nfoCommitAttemptPersistence{
		reserve: func(_ context.Context, v nfoCommitAttemptReservation) (nfoCommitAttemptReservation, error) {
			return v, saveOwnedAttempt(os.Getenv("JELEE_OWNED_ATTEMPT_RESERVATION"), ownedAttemptRecord(v))
		},
		progress: func(context.Context, nfoCommitFiles) error { return nil },
		ready:    func(context.Context, nfoCommitFiles) error { t.Fatal("helper reached ready"); return nil },
	}, ops)
	t.Fatal("helper returned before interruption")
}

func TestNFOCommitAttemptBoundsAndAmbiguousReservation(t *testing.T) {
	for _, kind := range []string{"zero", "exhausted", "unknown_response", "changed_budget", "changed_first_hash", "changed_number"} {
		t.Run(kind, func(t *testing.T) {
			path, root, original, replacement := commitFilesFixture(t)
			number := uint8(1)
			if kind == "zero" {
				number = 0
			}
			if kind == "exhausted" {
				number = 4
			}
			called := false
			_, err := prepareNFOCommitAttempt(context.Background(), root, "movie.nfo", original, replacement, commitFileToken(), number, nfoCommitAttemptPersistence{
				reserve: func(_ context.Context, v nfoCommitAttemptReservation) (nfoCommitAttemptReservation, error) {
					called = true
					switch kind {
					case "unknown_response":
						return v, errors.New("owned unknown result")
					case "changed_budget":
						v.retainedBytes--
					case "changed_first_hash":
						v.originalHash[0]++
					case "changed_number":
						v.plan.attempt++
					}
					return v, nil
				},
				progress: func(context.Context, nfoCommitFiles) error { t.Fatal("refused reservation progressed"); return nil },
				ready:    func(context.Context, nfoCommitFiles) error { t.Fatal("refused reservation reached ready"); return nil },
			}, nativeNFOWriteOperations())
			if err == nil {
				t.Fatal("invalid reservation admitted")
			}
			entries, e := os.ReadDir(path)
			if e != nil || len(entries) != 1 || entries[0].Name() != "movie.nfo" {
				t.Fatal("refused reservation changed filesystem")
			}
			if (kind == "zero" || kind == "exhausted") && called {
				t.Fatal("invalid ordinal reached persistence")
			}
		})
	}
}

func TestNFOCommitAttemptPrivacyAndNamespaceBounds(t *testing.T) {
	p := nfoCommitFilePlan{version: 1, token: commitFileToken(), filename: "movie.nfo"}
	names := map[string]bool{}
	for a := uint8(0); a <= maxNFOCommitAttempts; a++ {
		p.attempt = a
		for _, name := range p.names() {
			if names[name] {
				t.Fatal("namespace collision")
			}
			names[name] = true
		}
	}
	if len(names) != 20 {
		t.Fatal("bounded namespace inventory differs")
	}
	r := nfoCommitAttemptReservation{plan: p, retainedBytes: 123}
	for _, display := range []string{fmt.Sprint(r), fmt.Sprintf("%#v", r)} {
		if display != "nfo attempt reservation (redacted)" {
			t.Fatal("reservation exposes private fields")
		}
	}
}

func TestNFOCommitAttemptSavedPairResume(t *testing.T) {
	for _, phase := range []string{"output_pair", "complete_pair"} {
		t.Run(phase, func(t *testing.T) {
			_, root, original, replacement := commitFilesFixture(t)
			var first nfoCommitAttemptReservation
			var proof nfoCommitFiles
			reserve := func(_ context.Context, v nfoCommitAttemptReservation) (nfoCommitAttemptReservation, error) {
				if first == (nfoCommitAttemptReservation{}) {
					first = v
				}
				return first, nil
			}
			_, err := prepareNFOCommitAttempt(context.Background(), root, "movie.nfo", original, replacement, commitFileToken(), 2, nfoCommitAttemptPersistence{
				reserve: reserve,
				progress: func(_ context.Context, v nfoCommitFiles) error {
					proof = v
					if phase == "output_pair" || v.rollback != (nfoNativeIdentity{}) {
						return errors.New("owned ambiguous checkpoint")
					}
					return nil
				},
				ready: func(context.Context, nfoCommitFiles) error { t.Fatal("initial attempt reached ready"); return nil },
			}, nativeNFOWriteOperations())
			if !errors.Is(err, ErrReplace) || proof.output == (nfoNativeIdentity{}) {
				t.Fatal("checkpoint boundary missing")
			}
			before := ownedProgressArtifacts(t, root, proof.plan.names())
			ready := false
			_, err = prepareNFOCommitAttempt(context.Background(), root, "movie.nfo", original, replacement, commitFileToken(), 3, nfoCommitAttemptPersistence{reserve: reserve, resume: &proof, progress: func(context.Context, nfoCommitFiles) error { return nil }, ready: func(context.Context, nfoCommitFiles) error { return nil }}, nativeNFOWriteOperations())
			if err == nil || ownedProgressArtifacts(t, root, proof.plan.names()) != before {
				t.Fatal("different attempt adopted first proof")
			}
			files, err := prepareNFOCommitAttempt(context.Background(), root, "movie.nfo", original, replacement, commitFileToken(), 2, nfoCommitAttemptPersistence{
				reserve: reserve, resume: &proof,
				progress: func(_ context.Context, v nfoCommitFiles) error {
					if v.output != proof.output || v.plan != proof.plan {
						t.Fatal("resume changed first attempt proof")
					}
					return nil
				},
				ready: func(context.Context, nfoCommitFiles) error { ready = true; return nil },
			}, nativeNFOWriteOperations())
			if err != nil || files == nil || !ready || files.output != proof.output || verifyNFOCommitFiles(context.Background(), root, *files, original.original, replacement.original) != nil {
				t.Fatal("same attempt did not resume")
			}
			if phase == "complete_pair" && files.rollback != proof.rollback {
				t.Fatal("resume changed first rollback")
			}
		})
	}
}
