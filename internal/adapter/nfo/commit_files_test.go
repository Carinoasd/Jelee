//go:build windows || linux

package nfo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func commitFileToken() [16]byte {
	return [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
}

func commitFilesFixture(t *testing.T) (string, *os.Root, *Document, *Document) {
	t.Helper()
	return writeDocumentFixture(t)
}

func TestNFOCommitFilesPlanBeforeSideEffectsAndRetainedWitnesses(t *testing.T) {
	path, directory, original, replacement := commitFilesFixture(t)
	planned, ready := false, false
	files, err := prepareNFOCommitFiles(context.Background(), directory, "movie.nfo", original, replacement, commitFileToken(), nfoCommitFilePersistence{
		plan: func(ctx context.Context, plan nfoCommitFilePlan) error {
			entries, err := os.ReadDir(path)
			if err != nil || len(entries) != 1 || entries[0].Name() != "movie.nfo" {
				t.Fatal("file side effect happened before plan persistence")
			}
			planned = true
			return nil
		},
		ready: func(ctx context.Context, files nfoCommitFiles) error {
			if !planned || verifyNFOCommitFiles(ctx, directory, files, original.original, replacement.original) != nil {
				t.Fatal("ready persistence received incomplete files")
			}
			ready = true
			return nil
		},
	}, nativeNFOWriteOperations())
	if err != nil || files == nil || !ready {
		t.Fatal("commit file preparation failed", err)
	}
	for _, display := range []string{fmt.Sprint(files), fmt.Sprintf("%#v", files)} {
		if display != "nfo commit files (redacted)" {
			t.Fatal("commit file preparation exposed private records")
		}
	}
	checkCommitFilesProcess(t, path, *files, original.original, replacement.original)
	if data, err := directory.ReadFile("movie.nfo"); err != nil || !bytes.Equal(data, original.original) {
		t.Fatal("preparation modified target")
	}
	names := files.plan.names()
	// Removing one name retains the output inode through its saved witness.
	if err := directory.Rename(names[1], "movie.nfo"); err != nil {
		t.Fatal(err)
	}
	outputID, err := nativeIdentityWithin(directory, "movie.nfo")
	if err != nil || outputID != files.output {
		t.Fatal("rename did not retain prepared output identity")
	}
	if err := verifyNFOCommitFiles(context.Background(), directory, *files, original.original, replacement.original); !errors.Is(err, ErrChanged) {
		t.Fatal("missing stage was interpreted as a committed result", err)
	}
	for _, witness := range []string{names[0], names[2], names[4]} {
		if _, err := directory.Stat(witness); err != nil {
			t.Fatal("rename lost retained witness")
		}
	}
}

func TestNFOCommitFilesFailuresAndUnknownPersistence(t *testing.T) {
	for _, phase := range []string{"plan", "cancel_before_files", "output_sync", "rollback_sync", "directory_sync", "ready", "cancel_after_ready"} {
		t.Run(phase, func(t *testing.T) {
			path, directory, original, replacement := commitFilesFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var plan nfoCommitFilePlan
			readyCalled := false
			persist := nfoCommitFilePersistence{
				plan: func(_ context.Context, value nfoCommitFilePlan) error {
					plan = value
					if phase == "plan" {
						return errors.New("owned injected plan failure")
					}
					if phase == "cancel_before_files" {
						cancel()
					}
					return nil
				},
				ready: func(_ context.Context, _ nfoCommitFiles) error {
					readyCalled = true
					if phase == "ready" {
						return errors.New("owned injected ambiguous commit outcome")
					}
					if phase == "cancel_after_ready" {
						cancel()
					}
					return nil
				},
			}
			ops := nativeNFOWriteOperations()
			syncs := 0
			baseSync := ops.syncFile
			ops.syncFile = func(file *os.File) error {
				syncs++
				if phase == "output_sync" && syncs == 1 || phase == "rollback_sync" && syncs == 2 {
					return errors.New("owned injected stage failure")
				}
				return baseSync(file)
			}
			if phase == "directory_sync" {
				ops.syncDirectory = func(*os.Root) error { return errors.New("owned injected directory failure") }
			}
			files, err := prepareNFOCommitFiles(ctx, directory, "movie.nfo", original, replacement, commitFileToken(), persist, ops)
			if err == nil {
				t.Fatal("injected failure succeeded")
			}
			retain := phase == "ready" || phase == "cancel_after_ready"
			if readyCalled != retain || (files != nil) != retain {
				t.Fatal("persistence uncertainty lost evidence")
			}
			for _, name := range plan.names() {
				_, err := directory.Lstat(name)
				if retain && err != nil || !retain && !errors.Is(err, os.ErrNotExist) {
					t.Fatal("wrong witness cleanup outcome")
				}
			}
			if data, err := directory.ReadFile("movie.nfo"); err != nil || !bytes.Equal(data, original.original) {
				t.Fatal("failure modified target")
			}
			if retain {
				checkCommitFilesProcess(t, path, *files, original.original, replacement.original)
			}
		})
	}
}

func TestNFOCommitFilesCollisionAndExternalReplacement(t *testing.T) {
	for _, phase := range []string{"collision", "target_after_plan", "witness_after_ready", "in_place_after_ready"} {
		t.Run(phase, func(t *testing.T) {
			_, directory, original, replacement := commitFilesFixture(t)
			var plan nfoCommitFilePlan
			persist := nfoCommitFilePersistence{
				plan: func(_ context.Context, value nfoCommitFilePlan) error {
					plan = value
					if phase == "collision" {
						if err := directory.WriteFile(plan.names()[1], []byte("foreign owned fixture"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if phase == "target_after_plan" {
						if err := directory.Rename("movie.nfo", "old.nfo"); err != nil {
							t.Fatal(err)
						}
						if err := directory.WriteFile("movie.nfo", original.original, 0600); err != nil {
							t.Fatal(err)
						}
					}
					return nil
				},
				ready: func(context.Context, nfoCommitFiles) error { return nil },
			}
			files, err := prepareNFOCommitFiles(context.Background(), directory, "movie.nfo", original, replacement, commitFileToken(), persist, nativeNFOWriteOperations())
			if phase == "in_place_after_ready" {
				if err != nil {
					t.Fatal(err)
				}
				if err := directory.WriteFile("movie.nfo", []byte("<movie><title>external</title></movie>"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := verifyNFOCommitFiles(context.Background(), directory, *files, original.original, replacement.original); !errors.Is(err, ErrChanged) {
					t.Fatal("in-place original modification accepted", err)
				}
				data, err := directory.ReadFile(plan.names()[3])
				if err != nil || !bytes.Equal(data, original.original) {
					t.Fatal("external source edit destroyed independent rollback copy")
				}
			} else if phase == "witness_after_ready" {
				if err != nil {
					t.Fatal(err)
				}
				name := plan.names()[2]
				if err := directory.Rename(name, name+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := directory.WriteFile(name, replacement.original, 0600); err != nil {
					t.Fatal(err)
				}
				if err := verifyNFOCommitFiles(context.Background(), directory, *files, original.original, replacement.original); !errors.Is(err, ErrChanged) {
					t.Fatal("same-byte witness replacement accepted", err)
				}
			} else if err == nil || files != nil {
				t.Fatal("external collision or source replacement accepted")
			}
			if phase == "collision" {
				data, err := directory.ReadFile(plan.names()[1])
				if err != nil || string(data) != "foreign owned fixture" {
					t.Fatal("foreign collision overwritten or deleted")
				}
			}
		})
	}
}

type commitFilesProcessRecord struct {
	Version                          uint8
	Token                            [16]byte
	Filename                         string
	Parent, Target, Output, Rollback [48]byte
	OriginalBytes, ReplacementBytes  []byte
}

func checkCommitFilesProcess(t *testing.T, path string, files nfoCommitFiles, original, replacement []byte) {
	t.Helper()
	record := commitFilesProcessRecord{files.plan.version, files.plan.token, files.plan.filename, files.plan.parent.record, files.plan.target.record, files.output.record, files.rollback.record, original, replacement}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(t.TempDir(), "private-record.json")
	if err := os.WriteFile(saved, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNFOCommitFilesProcessHelper$")
	cmd.Env = append(os.Environ(), "JELEE_NFO_COMMIT_FILES_TEST_ROOT="+path, "JELEE_NFO_COMMIT_FILES_TEST_RECORD="+saved)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit files cross-process verification failed: %v %s", err, out)
	}
}

func TestNFOCommitFilesProcessHelper(t *testing.T) {
	path := os.Getenv("JELEE_NFO_COMMIT_FILES_TEST_ROOT")
	if path == "" {
		return
	}
	data, err := os.ReadFile(os.Getenv("JELEE_NFO_COMMIT_FILES_TEST_RECORD"))
	var record commitFilesProcessRecord
	if err != nil || json.Unmarshal(data, &record) != nil {
		t.Fatal("cannot read owned persisted fixture")
	}
	identities := make([]nfoNativeIdentity, 4)
	for i, encoded := range [][48]byte{record.Parent, record.Target, record.Output, record.Rollback} {
		identities[i], err = parseNFONativeIdentity(encoded[:])
		if err != nil {
			t.Fatal(err)
		}
	}
	directory, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal("cannot reopen owned commit root")
	}
	defer directory.Close()
	files := nfoCommitFiles{nfoCommitFilePlan{version: record.Version, token: record.Token, filename: record.Filename, parent: identities[0], target: identities[1]}, identities[2], identities[3]}
	if err := verifyNFOCommitFiles(context.Background(), directory, files, record.OriginalBytes, record.ReplacementBytes); err != nil {
		t.Fatal("retained physical witnesses failed cross-process verification", err)
	}
}

func TestNFOCommitFilesBindBytesAndNativeObservation(t *testing.T) {
	_, directory, original, _ := commitFilesFixture(t)
	observed, err := verifyNFOOriginal(context.Background(), directory, "movie.nfo", original.original, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Rename("movie.nfo", "old.nfo"); err != nil {
		t.Fatal(err)
	}
	if err := directory.WriteFile("movie.nfo", original.original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeIdentityWithinInfo(directory, "movie.nfo", observed); !errors.Is(err, ErrChanged) {
		t.Fatal("byte observation and native identity came from different objects", err)
	}
}
