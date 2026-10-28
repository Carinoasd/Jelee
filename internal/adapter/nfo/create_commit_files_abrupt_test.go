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
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// These regressions kill a real child process (os.Exit, no deferred cleanup) at
// every boundary of a create: plan, output create/sync, pin, directory sync,
// checkpoint, ready, phase 1, the link, its directory sync and phase 2. A fresh
// process then recovers from the evidence the child had saved, as the Writer
// does: resume the attempt when its checkpoint exists, otherwise allocate the
// next namespace, and settle from the observed target. The result is either a
// target created exactly once with the prepared bytes, or no target, and an
// object that appeared at the name is never touched. The file-backed evidence is
// an owned fixture, not PostgreSQL.

type createCrashState struct {
	Plan *struct {
		Version  uint8
		Token    [16]byte
		Filename string
		Parent   [48]byte
		Attempt  uint8
	}
	Checkpoint *[48]byte
	Ready      bool
	Phases     []int
}

func (s *createCrashState) save(path string) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func loadCreateCrashState(t *testing.T, path string) *createCrashState {
	t.Helper()
	state := &createCrashState{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state
	}
	if err != nil || json.Unmarshal(data, state) != nil {
		t.Fatal("abrupt child lost its saved evidence")
	}
	return state
}

func (s *createCrashState) hasPhase(p int) bool {
	for _, phase := range s.Phases {
		if phase == p {
			return true
		}
	}
	return false
}

func (s *createCrashState) files() *nfoCommitFiles {
	plan := nfoCommitFilePlan{version: s.Plan.Version, token: s.Plan.Token, filename: s.Plan.Filename, parent: nfoNativeIdentity{record: s.Plan.Parent}, attempt: s.Plan.Attempt}
	files := &nfoCommitFiles{plan: plan}
	if s.Checkpoint != nil {
		files.output = nfoNativeIdentity{record: *s.Checkpoint}
	}
	return files
}

// exitCodes names every kill point and its exit code.
var createCrashPoints = []struct {
	point string
	code  int
}{
	{"plan", 80}, {"output_created", 91}, {"output_written", 81}, {"output_synced", 82}, {"pin_linked", 83}, {"directory_synced", 84}, {"checkpoint", 85}, {"ready", 86},
	{"phase1", 87}, {"linked", 88}, {"link_synced", 89}, {"phase2", 90},
}

func TestNFOCreateAbruptProcessRecovery(t *testing.T) {
	for _, interference := range []string{"none", "foreign target appears"} {
		for _, c := range createCrashPoints {
			t.Run(c.point+"/"+interference, func(t *testing.T) {
				f := newCreateFixture(t)
				dir := t.TempDir()
				statePath, inputPath := filepath.Join(dir, "state.json"), filepath.Join(dir, "input.bin")
				if err := os.WriteFile(inputPath, f.replacement.original, 0o600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNFOCreateAbruptHelper$", "-test.count=1")
				cmd.Env = append(os.Environ(), "JELEE_CREATE_ROOT="+f.path, "JELEE_CREATE_STATE="+statePath, "JELEE_CREATE_INPUT="+inputPath, "JELEE_CREATE_POINT="+c.point)
				output, err := cmd.CombinedOutput()
				_ = output // Never print private child diagnostics or payloads.
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != c.code {
					t.Fatal("abrupt helper did not reach the selected boundary", c.point)
				}
				state := loadCreateCrashState(t, statePath)
				linked := c.point == "linked" || c.point == "link_synced" || c.point == "phase2"
				if linked != f.targetExists() {
					t.Fatal("the crash left an unexpected target state", c.point)
				}
				var foreign []byte
				if interference != "none" {
					foreign = []byte("<movie><title>appeared during the crash</title></movie>")
					target := filepath.Join(f.path, createTarget)
					replaced := filepath.Join(f.path, "foreign.tmp")
					if err := os.WriteFile(replaced, foreign, 0o600); err != nil {
						t.Fatal(err)
					}
					// An object that exists is replaced by rename, as a user could.
					if err := os.Rename(replaced, target); err != nil {
						t.Fatal(err)
					}
				}
				recoverCreate(t, f, state, foreign)
			})
		}
	}
}

// recoverCreate is what a fresh process does with the evidence of a dead one.
func recoverCreate(t *testing.T, f *createFixture, state *createCrashState, foreign []byte) {
	t.Helper()
	target := filepath.Join(f.path, createTarget)
	ctx := context.Background()
	var final *nfoCommitFiles
	attempts := map[uint8]nfoNativeIdentity{}
	record := func(files nfoCommitFiles) {
		attempts[files.plan.attempt] = files.output
	}
	if state.Plan != nil && state.Checkpoint != nil {
		record(*state.files())
	}
	var stageErr error
	switch {
	case state.hasPhase(2):
		final = state.files()
	case state.Plan == nil:
		// The child never persisted a plan: nothing was created.
		final, stageErr = f.stage(nativeNFOWriteOperations())
	case state.Ready || state.Checkpoint != nil && state.hasPhase(1):
		final = state.files()
		stageErr = verifyNFOCreateFiles(ctx, f.directory, *final, f.replacement.original)
		if state.hasPhase(1) && stageErr != nil && errors.Is(stageErr, ErrTargetAppeared) {
			stageErr = nil
		}
	case state.Checkpoint != nil:
		persist := f.persistence(state.Plan.Attempt)
		persist.resume = state.files()
		final, stageErr = prepareNFOCreateFiles(ctx, f.directory, createTarget, f.replacement, state.Plan.Token, persist, nativeNFOWriteOperations())
	default:
		// No checkpoint: names of the attempt may exist and are never adopted.
		attempt := state.Plan.Attempt
		plan := nfoCommitFilePlan{token: state.Plan.Token, attempt: attempt}
		present := false
		for _, name := range plan.names() {
			if _, err := f.directory.Lstat(name); err == nil {
				present = true
			}
		}
		if present {
			attempt++
		}
		final, stageErr = prepareNFOCreateFiles(ctx, f.directory, createTarget, f.replacement, state.Plan.Token, f.persistence(attempt), nativeNFOWriteOperations())
	}
	if final != nil && final.output != (nfoNativeIdentity{}) {
		record(*final)
	}
	if foreign != nil && !state.hasPhase(2) && !state.hasPhase(1) && stageErr != nil {
		// A target that appeared before phase 1: the stage refuses, nothing linked.
		if !errors.Is(stageErr, ErrTargetAppeared) {
			t.Fatal("a foreign target was not reported at stage", stageErr)
		}
		final = nil
	} else if stageErr != nil && !(foreign != nil && state.hasPhase(1)) {
		t.Fatal("recovery staging failed", stageErr)
	}
	var phases []nfoSettlePhase
	if !state.hasPhase(2) && (final != nil && stageErr == nil || final != nil && foreign != nil && state.hasPhase(1)) {
		progress := func(_ context.Context, phase nfoSettlePhase) error {
			phases = append(phases, phase)
			return nil
		}
		phase, err := settleNFOCreateFiles(ctx, f.directory, *final, f.replacement.original, state.hasPhase(1), progress, nativeNFOWriteOperations())
		switch {
		case foreign != nil:
			if !errors.Is(err, ErrTargetAppeared) || phase == nfoSettleReplaced {
				t.Fatal("recovery did not stop at the foreign object", phase, err)
			}
		case err != nil || phase != nfoSettleReplaced:
			t.Fatal("recovery did not finish the create", phase, err)
		}
	}
	if foreign != nil {
		if got := readFileOrFail(t, target); !bytes.Equal(got, foreign) {
			t.Fatal("the foreign object was touched by recovery")
		}
	} else {
		// Created exactly once with the prepared bytes.
		if got := readFileOrFail(t, target); !bytes.Equal(got, f.replacement.original) {
			t.Fatal("the created file differs from the prepared bytes")
		}
		current, err := nativeIdentityWithin(f.directory, createTarget)
		if err != nil || current != attempts[final.plan.attempt] {
			t.Fatal("the created target is not the recorded output of the final attempt")
		}
	}
	// The cleaner removes token names whose identity was recorded and retains
	// the rest; the target is never among them.
	token := state.Plan
	if token == nil {
		token = &struct {
			Version  uint8
			Token    [16]byte
			Filename string
			Parent   [48]byte
			Attempt  uint8
		}{Token: commitFileToken(), Filename: createTarget}
		parent, err := nativeIdentityWithin(f.directory, ".")
		if err != nil {
			t.Fatal(err)
		}
		token.Parent = parent.record
	}
	input := domain.NFOWriteCommitCleanupInput{
		Lease:        domain.NFOWriteCommitCleanupLease{Token: cleanupTokenID(token.Token)},
		RootPath:     filepath.Dir(f.path),
		RelativePath: filepath.Base(f.path) + "/" + createTarget,
		Plan:         domain.NFOWriteCommitFilePlan{Version: domain.NFOWriteCommitCreatePlanVersion, TargetName: createTarget, ParentIdentity: token.Parent},
	}
	for attempt := uint8(0); attempt <= maxNFOCommitAttempts; attempt++ {
		namespace := domain.NFOWriteCommitCleanupNamespace{Attempt: attempt}
		if id, ok := attempts[attempt]; ok {
			namespace.OutputIdentity = id.record
		}
		input.Namespaces = append(input.Namespaces, namespace)
	}
	_, retained, err := cleanupWriter(t).CleanNFOWriteCommitFiles(ctx, input)
	if err != nil {
		t.Fatal("cleanup after recovery failed", err)
	}
	for _, name := range f.names() {
		switch {
		case name == createTarget:
		case strings.HasPrefix(name, ".jelee-nfo-commit-") && retained > 0:
			// A name whose creation was never recorded is kept, not guessed.
		default:
			t.Fatal("cleanup left or removed an unexpected name", name)
		}
	}
	if foreign != nil {
		if got := readFileOrFail(t, target); !bytes.Equal(got, foreign) {
			t.Fatal("cleanup touched the foreign object")
		}
	} else if got := readFileOrFail(t, target); !bytes.Equal(got, f.replacement.original) {
		t.Fatal("cleanup removed or changed the created target")
	}
}

func TestNFOCreateAbruptHelper(t *testing.T) {
	path := os.Getenv("JELEE_CREATE_ROOT")
	if path == "" {
		return
	}
	point := os.Getenv("JELEE_CREATE_POINT")
	statePath := os.Getenv("JELEE_CREATE_STATE")
	code := map[string]int{}
	for _, c := range createCrashPoints {
		code[c.point] = c.code
	}
	directory, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal("owned fixture operation failed")
	}
	defer func() { _ = directory.Close() }()
	input, err := os.ReadFile(os.Getenv("JELEE_CREATE_INPUT"))
	if err != nil {
		t.Fatal("owned fixture operation failed")
	}
	replacement := editDocument(t, input)
	state := &createCrashState{}
	exit := func(at string) {
		if at == point {
			os.Exit(code[at])
		}
	}
	ops := nativeNFOWriteOperations()
	ops.created = func() { exit("output_created") }
	syncFile, syncDirectory := ops.syncFile, ops.syncDirectory
	syncs, dirSyncs := 0, 0
	ops.syncFile = func(file *os.File) error {
		syncs++
		if syncs == 1 {
			exit("output_written")
		}
		if err := syncFile(file); err != nil {
			return err
		}
		if syncs == 1 {
			exit("output_synced")
		}
		return nil
	}
	ops.syncDirectory = func(root *os.Root) error {
		dirSyncs++
		if dirSyncs == 1 {
			// The pin is linked right before the first directory sync.
			exit("pin_linked")
		}
		if err := syncDirectory(root); err != nil {
			return err
		}
		switch dirSyncs {
		case 1:
			exit("directory_synced")
		case 2:
			exit("link_synced")
		}
		return nil
	}
	link := ops.link
	ops.link = func(root *os.Root, from, to string) error {
		if err := link(root, from, to); err != nil {
			return err
		}
		exit("linked")
		return nil
	}
	token := commitFileToken()
	files, err := prepareNFOCreateFiles(context.Background(), directory, createTarget, replacement, token, nfoCommitFilePersistence{
		plan: func(_ context.Context, p nfoCommitFilePlan) error {
			state.Plan = &struct {
				Version  uint8
				Token    [16]byte
				Filename string
				Parent   [48]byte
				Attempt  uint8
			}{p.version, p.token, p.filename, p.parent.record, p.attempt}
			if err := state.save(statePath); err != nil {
				return err
			}
			exit("plan")
			return nil
		},
		progress: func(_ context.Context, f nfoCommitFiles) error {
			output := f.output.record
			state.Checkpoint = &output
			if err := state.save(statePath); err != nil {
				return err
			}
			exit("checkpoint")
			return nil
		},
		ready: func(context.Context, nfoCommitFiles) error {
			state.Ready = true
			if err := state.save(statePath); err != nil {
				return err
			}
			exit("ready")
			return nil
		},
	}, ops)
	if err != nil {
		t.Fatal("abrupt helper stage failed")
	}
	progress := func(_ context.Context, phase nfoSettlePhase) error {
		recorded := 1
		if phase == nfoSettleReplaced {
			recorded = 2
		}
		state.Phases = append(state.Phases, recorded)
		if err := state.save(statePath); err != nil {
			return err
		}
		if phase == nfoSettleBackedUp {
			exit("phase1")
		} else {
			exit("phase2")
		}
		return nil
	}
	if _, err := settleNFOCreateFiles(context.Background(), directory, *files, replacement.original, false, progress, ops); err != nil {
		t.Fatal("abrupt helper settle failed")
	}
	t.Fatal("abrupt helper finished without exiting at its boundary")
}
