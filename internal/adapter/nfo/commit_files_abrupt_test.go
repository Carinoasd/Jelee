//go:build windows || linux

package nfo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This regression records the current incomplete recovery boundary: an abrupt
// process exit bypasses in-memory cleanup, and a fresh process must retain every
// unknown sidecar. It does not establish automatic continuation or power-loss
// durability. The file-backed callback is an owned fixture, not PostgreSQL.

func TestNFOCommitFilesAbruptPartialProcess(t *testing.T) {
	for _, phase := range []string{"plan_sync", "output_sync", "rollback_sync", "directory_sync"} {
		t.Run(phase, func(t *testing.T) {
			path, directory, original, replacement := commitFilesFixture(t)
			inputPath := filepath.Join(t.TempDir(), "input.json")
			planPath := filepath.Join(t.TempDir(), "plan.json")
			input, err := json.Marshal(struct{ Original, Replacement []byte }{original.original, replacement.original})
			if err != nil {
				t.Fatal("owned fixture operation failed")
			}
			if err := os.WriteFile(inputPath, input, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNFOCommitFilesAbruptPartialHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "JELEE_OWNED_PARTIAL_ROOT="+path, "JELEE_OWNED_PARTIAL_INPUT="+inputPath, "JELEE_OWNED_PARTIAL_PLAN="+planPath, "JELEE_OWNED_PARTIAL_PHASE="+phase)
			output, err := cmd.CombinedOutput()
			_ = output // Never print private child diagnostics or payloads.
			var exited *exec.ExitError
			exitCode, present := 71, 2
			if phase == "plan_sync" {
				exitCode, present = 70, 0
			}
			if phase == "rollback_sync" {
				exitCode, present = 72, 4
			}
			if phase == "directory_sync" {
				exitCode, present = 73, 5
			}
			if !errors.As(err, &exited) || exited.ExitCode() != exitCode {
				t.Fatal("owned abrupt helper did not reach selected sync boundary")
			}
			encoded, err := os.ReadFile(planPath)
			if err != nil {
				t.Fatal("abrupt exit lost saved owned plan")
			}
			var record struct {
				Version        uint8
				Token          [16]byte
				Filename       string
				Parent, Target [48]byte
			}
			if err := json.Unmarshal(encoded, &record); err != nil {
				t.Fatal(err)
			}
			plan := nfoCommitFilePlan{version: record.Version, token: record.Token, filename: record.Filename, parent: nfoNativeIdentity{record: record.Parent}, target: nfoNativeIdentity{record: record.Target}}
			names := plan.names()
			before := make(map[string]nfoNativeIdentity)
			for i, name := range names {
				if i < present {
					identity, err := nativeIdentityWithin(directory, name)
					if err != nil {
						t.Fatal("abrupt exit lost partial witness")
					}
					before[name] = identity
					expected := original.original
					if i == 1 || i == 2 {
						expected = replacement.original
					}
					data, err := directory.ReadFile(name)
					if err != nil || !bytes.Equal(data, expected) {
						t.Fatal("abrupt partial bytes differ")
					}
				} else if _, err := directory.Lstat(name); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("helper unexpectedly reached later stage artifacts")
				}
			}
			fresh, err := os.OpenRoot(path)
			if err != nil {
				t.Fatal("owned fixture operation failed")
			}
			defer fresh.Close()
			readyCalled := false
			files, err := prepareNFOCommitFiles(context.Background(), fresh, "movie.nfo", original, replacement, commitFileToken(), nfoCommitFilePersistence{
				plan: func(_ context.Context, p nfoCommitFilePlan) error {
					if p != plan {
						t.Fatal("retry changed first partial plan")
					}
					return nil
				},
				ready: func(context.Context, nfoCommitFiles) error { readyCalled = true; return nil },
			}, nativeNFOWriteOperations())
			if phase == "plan_sync" {
				if err != nil || files == nil || !readyCalled || verifyNFOCommitFiles(context.Background(), fresh, *files, original.original, replacement.original) != nil {
					t.Fatal("saved plan without file effects did not continue")
				}
			} else if !errors.Is(err, ErrReplace) || files != nil || readyCalled {
				t.Fatal("partial collision was guessed to be ready")
			}
			for name, identity := range before {
				current, err := nativeIdentityWithin(fresh, name)
				if err != nil || current != identity {
					t.Fatal("retry removed or replaced retained partial evidence")
				}
			}
			data, err := fresh.ReadFile("movie.nfo")
			if err != nil || !bytes.Equal(data, original.original) {
				t.Fatal("partial retry changed original target")
			}
		})
	}
}

func TestNFOCommitFilesAbruptPartialHelper(t *testing.T) {
	path := os.Getenv("JELEE_OWNED_PARTIAL_ROOT")
	if path == "" {
		return
	}
	directory, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal("owned fixture operation failed")
	}
	defer directory.Close()
	input, err := os.ReadFile(os.Getenv("JELEE_OWNED_PARTIAL_INPUT"))
	if err != nil {
		t.Fatal("owned fixture operation failed")
	}
	// Windows syncDirectory is currently a stub; its hook demonstrates process
	// interruption before ready, not filesystem metadata durability.
	var recipe struct{ Original, Replacement []byte }
	if err := json.Unmarshal(input, &recipe); err != nil {
		t.Fatal(err)
	}
	original, replacement := editDocument(t, recipe.Original), editDocument(t, recipe.Replacement)
	ops := nativeNFOWriteOperations()
	phase := os.Getenv("JELEE_OWNED_PARTIAL_PHASE")
	sync, syncs := ops.syncFile, 0
	ops.syncFile = func(file *os.File) error {
		if err := sync(file); err != nil {
			return err
		}
		syncs++
		if phase == "output_sync" && syncs == 1 {
			os.Exit(71)
		}
		if phase == "rollback_sync" && syncs == 2 {
			os.Exit(72)
		}
		return nil
	}
	syncDirectory := ops.syncDirectory
	ops.syncDirectory = func(directory *os.Root) error {
		if err := syncDirectory(directory); err != nil {
			return err
		}
		if phase == "directory_sync" {
			os.Exit(73)
		}
		return nil
	}
	_, err = prepareNFOCommitFiles(context.Background(), directory, "movie.nfo", original, replacement, commitFileToken(), nfoCommitFilePersistence{
		plan: func(_ context.Context, p nfoCommitFilePlan) error {
			data, err := json.Marshal(struct {
				Version        uint8
				Token          [16]byte
				Filename       string
				Parent, Target [48]byte
			}{p.version, p.token, p.filename, p.parent.record, p.target.record})
			if err != nil {
				return err
			}
			file, err := os.OpenFile(os.Getenv("JELEE_OWNED_PARTIAL_PLAN"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err := file.Write(data); err != nil {
				return err
			}
			if err := file.Sync(); err != nil {
				return err
			}
			if phase == "plan_sync" {
				os.Exit(70)
			}
			return nil
		},
		ready: func(context.Context, nfoCommitFiles) error { t.Fatal("abrupt helper reached ready"); return nil },
	}, ops)
	t.Fatal("abrupt helper returned before crash boundary")
}
