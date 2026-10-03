//go:build windows || linux

package nfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type ownedCommitProgress struct {
	Version                          uint8
	Token                            [16]byte
	Filename                         string
	Parent, Target, Output, Rollback [48]byte
}

func (r ownedCommitProgress) files() nfoCommitFiles {
	return nfoCommitFiles{plan: nfoCommitFilePlan{r.Version, r.Token, r.Filename, nfoNativeIdentity{r.Parent}, nfoNativeIdentity{r.Target}}, output: nfoNativeIdentity{r.Output}, rollback: nfoNativeIdentity{r.Rollback}}
}

func TestNFOCommitProgressProcessResume(t *testing.T) {
	for _, phase := range []string{"output_pair", "complete_pair"} {
		t.Run(phase, func(t *testing.T) {
			path, directory, original, replacement := commitFilesFixture(t)
			recipePath, evidencePath := filepath.Join(t.TempDir(), "recipe.json"), filepath.Join(t.TempDir(), "progress.jsonl")
			recipe, err := json.Marshal(struct{ Original, Replacement []byte }{original.original, replacement.original})
			if err != nil || os.WriteFile(recipePath, recipe, 0600) != nil {
				t.Fatal("owned recipe save failed")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNFOCommitProgressProcessHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "JELEE_OWNED_PROGRESS_ROOT="+path, "JELEE_OWNED_PROGRESS_RECIPE="+recipePath, "JELEE_OWNED_PROGRESS_EVIDENCE="+evidencePath, "JELEE_OWNED_PROGRESS_PHASE="+phase)
			diagnostics, err := cmd.CombinedOutput()
			_ = diagnostics // Never expose the private input or child diagnostics.
			var exited *exec.ExitError
			exitCode, records := 80, 1
			if phase == "complete_pair" {
				exitCode, records = 81, 2
			}
			if !errors.As(err, &exited) || exited.ExitCode() != exitCode {
				t.Fatal("child did not reach committed progress boundary")
			}
			encoded, err := os.ReadFile(evidencePath)
			if err != nil {
				t.Fatal("progress evidence missing")
			}
			lines := bytes.Split(bytes.TrimSpace(encoded), []byte("\n"))
			if len(lines) != records {
				t.Fatal("progress checkpoint count differs")
			}
			var saved ownedCommitProgress
			if json.Unmarshal(lines[len(lines)-1], &saved) != nil {
				t.Fatal("progress decode failed")
			}
			progress := saved.files()
			beforeOutput := progress.output
			fresh, err := os.OpenRoot(path)
			if err != nil {
				t.Fatal("fresh root open failed")
			}
			defer fresh.Close()
			ready := false
			files, err := prepareNFOCommitFiles(context.Background(), fresh, "movie.nfo", original, replacement, commitFileToken(), nfoCommitFilePersistence{
				resume: &progress,
				plan: func(_ context.Context, p nfoCommitFilePlan) error {
					if p != progress.plan {
						t.Fatal("resume changed first plan")
					}
					return nil
				},
				progress: func(_ context.Context, p nfoCommitFiles) error {
					if p.plan != progress.plan || p.output != beforeOutput || p.rollback == (nfoNativeIdentity{}) {
						t.Fatal("resume changed first output or regressed progress")
					}
					return nil
				},
				ready: func(_ context.Context, p nfoCommitFiles) error { ready = true; return nil },
			}, nativeNFOWriteOperations())
			if err != nil || files == nil || !ready || files.output != beforeOutput || verifyNFOCommitFiles(context.Background(), directory, *files, original.original, replacement.original) != nil {
				t.Fatal("committed partial checkpoint did not resume to ready")
			}
			if phase == "complete_pair" && files.rollback != progress.rollback {
				t.Fatal("resume changed first rollback")
			}
			if data, err := fresh.ReadFile("movie.nfo"); err != nil || !bytes.Equal(data, original.original) {
				t.Fatal("resume changed target")
			}
		})
	}
}

func TestNFOCommitProgressProcessHelper(t *testing.T) {
	path := os.Getenv("JELEE_OWNED_PROGRESS_ROOT")
	if path == "" {
		return
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal("owned root unavailable")
	}
	defer root.Close()
	encoded, err := os.ReadFile(os.Getenv("JELEE_OWNED_PROGRESS_RECIPE"))
	if err != nil {
		t.Fatal("owned recipe unavailable")
	}
	var recipe struct{ Original, Replacement []byte }
	if json.Unmarshal(encoded, &recipe) != nil {
		t.Fatal("owned recipe decode failed")
	}
	original, replacement := editDocument(t, recipe.Original), editDocument(t, recipe.Replacement)
	_, err = prepareNFOCommitFiles(context.Background(), root, "movie.nfo", original, replacement, commitFileToken(), nfoCommitFilePersistence{
		plan: func(context.Context, nfoCommitFilePlan) error { return nil },
		progress: func(_ context.Context, p nfoCommitFiles) error {
			record := ownedCommitProgress{p.plan.version, p.plan.token, p.plan.filename, p.plan.parent.record, p.plan.target.record, p.output.record, p.rollback.record}
			data, err := json.Marshal(record)
			if err != nil {
				return err
			}
			file, err := os.OpenFile(os.Getenv("JELEE_OWNED_PROGRESS_EVIDENCE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err := file.Write(append(data, '\n')); err != nil {
				return err
			}
			if err := file.Sync(); err != nil {
				return err
			}
			if os.Getenv("JELEE_OWNED_PROGRESS_PHASE") == "output_pair" {
				os.Exit(80)
			}
			if p.rollback != (nfoNativeIdentity{}) {
				os.Exit(81)
			}
			return nil
		},
		ready: func(context.Context, nfoCommitFiles) error { t.Fatal("helper unexpectedly reached ready"); return nil },
	}, nativeNFOWriteOperations())
	_ = err
	t.Fatal("helper returned before selected process exit")
}

func TestNFOCommitProgressRejectsUnprovenObjects(t *testing.T) {
	for _, mutation := range []string{"changed_bytes", "missing_witness", "same_bytes_new_inode", "unknown_rollback", "unknown_rollback_pin", "wrong_plan", "wrong_output_identity"} {
		t.Run(mutation, func(t *testing.T) {
			_, root, original, replacement := commitFilesFixture(t)
			var saved nfoCommitFiles
			_, err := prepareNFOCommitFiles(context.Background(), root, "movie.nfo", original, replacement, commitFileToken(), nfoCommitFilePersistence{
				plan: func(context.Context, nfoCommitFilePlan) error { return nil },
				progress: func(_ context.Context, p nfoCommitFiles) error {
					saved = p
					return errors.New("owned unknown progress response")
				},
				ready: func(context.Context, nfoCommitFiles) error { t.Fatal("initial attempt reached ready"); return nil },
			}, nativeNFOWriteOperations())
			if !errors.Is(err, ErrReplace) || saved.output == (nfoNativeIdentity{}) || saved.rollback != (nfoNativeIdentity{}) {
				t.Fatal("unknown checkpoint response did not retain output pair")
			}
			names := saved.plan.names()
			switch mutation {
			case "changed_bytes":
				if root.WriteFile(names[1], []byte("owned tamper"), 0600) != nil {
					t.Fatal("tamper failed")
				}
			case "missing_witness":
				if root.Remove(names[2]) != nil {
					t.Fatal("witness removal failed")
				}
			case "same_bytes_new_inode":
				if root.Rename(names[1], "owned-retained-output") != nil || root.WriteFile(names[1], replacement.original, 0600) != nil {
					t.Fatal("replacement failed")
				}
			case "unknown_rollback":
				if root.WriteFile(names[3], original.original, 0600) != nil {
					t.Fatal("unknown stage creation failed")
				}
			case "unknown_rollback_pin":
				if root.Link("movie.nfo", names[4]) != nil {
					t.Fatal("unknown witness creation failed")
				}
			case "wrong_plan":
				saved.plan.token[0]++
			case "wrong_output_identity":
				saved.output = saved.plan.target
			}
			before, err := root.ReadFile("movie.nfo")
			if err != nil {
				t.Fatal("target read failed")
			}
			artifactsBefore := ownedProgressArtifacts(t, root, names)
			progressCalled, readyCalled := false, false
			_, err = prepareNFOCommitFiles(context.Background(), root, "movie.nfo", original, replacement, commitFileToken(), nfoCommitFilePersistence{
				resume:   &saved,
				plan:     func(context.Context, nfoCommitFilePlan) error { return nil },
				progress: func(context.Context, nfoCommitFiles) error { progressCalled = true; return nil },
				ready:    func(context.Context, nfoCommitFiles) error { readyCalled = true; return nil },
			}, nativeNFOWriteOperations())
			if artifactsAfter := ownedProgressArtifacts(t, root, names); artifactsAfter != artifactsBefore {
				t.Fatal("rejected resume changed retained artifacts")
			}
			if err == nil || progressCalled || readyCalled {
				t.Fatal("unproven partial objects admitted")
			}
			after, err := root.ReadFile("movie.nfo")
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("rejected resume changed target")
			}
			if mutation == "unknown_rollback" {
				data, err := root.ReadFile(names[3])
				if err != nil || !bytes.Equal(data, original.original) {
					t.Fatal("unknown rollback was removed or rewritten")
				}
			}
		})
	}
}

type ownedProgressArtifact struct {
	Present  bool
	Identity nfoNativeIdentity
	Digest   [32]byte
}

func ownedProgressArtifacts(t *testing.T, root *os.Root, names [5]string) (result [5]ownedProgressArtifact) {
	t.Helper()
	for i, name := range names {
		if _, err := root.Lstat(name); errors.Is(err, os.ErrNotExist) {
			continue
		}
		identity, err := nativeIdentityWithin(root, name)
		if err != nil {
			t.Fatal("artifact observation failed")
		}
		data, err := root.ReadFile(name)
		if err != nil {
			t.Fatal("artifact bytes unavailable")
		}
		result[i] = ownedProgressArtifact{true, identity, sha256.Sum256(data)}
	}
	return result
}
