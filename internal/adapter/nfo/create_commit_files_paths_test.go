//go:build linux

package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestNFOCreateFilesSoundRejectsEveryOtherShape(t *testing.T) {
	f := newCreateFixture(t)
	files := *f.mustStage()
	if !nfoCreateFilesSound(files) {
		t.Fatal("a staged create must be sound")
	}
	withRewritePlan := files
	withRewritePlan.plan.version = domain.NFOWriteCommitPlanVersion
	withTarget := files
	withTarget.plan.target = files.output
	withRollback := files
	withRollback.rollback = files.output
	withoutOutput := files
	withoutOutput.output = nfoNativeIdentity{}
	for name, bad := range map[string]nfoCommitFiles{"rewrite plan": withRewritePlan, "target identity": withTarget, "rollback identity": withRollback, "no output": withoutOutput} {
		if nfoCreateFilesSound(bad) {
			t.Fatal("accepted an unsound create:", name)
		}
	}
}

func TestObserveNFOCreateTargetClassifiesTheName(t *testing.T) {
	f := newCreateFixture(t)
	files := f.mustStage()
	output := files.output
	if state, err := observeNFOCreateTarget(f.directory, createTarget, output); err != nil || state != nfoCreateTargetAbsent {
		t.Fatal("absent target", state, err)
	}
	// The output's own staging name is the output object: ours.
	if state, err := observeNFOCreateTarget(f.directory, files.plan.names()[1], output); err != nil || state != nfoCreateTargetOurs {
		t.Fatal("own object", state, err)
	}
	// The pin is the same object too.
	if state, err := observeNFOCreateTarget(f.directory, files.plan.names()[2], output); err != nil || state != nfoCreateTargetOurs {
		t.Fatal("pin", state, err)
	}
	// Any other object is foreign, even with the same bytes.
	if err := os.WriteFile(filepath.Join(f.path, createTarget), f.replacement.original, 0o600); err != nil {
		t.Fatal(err)
	}
	if state, err := observeNFOCreateTarget(f.directory, createTarget, output); err != nil || state != nfoCreateTargetForeign {
		t.Fatal("byte-identical foreign object", state, err)
	}
	// A directory is foreign as well.
	if err := os.Mkdir(filepath.Join(f.path, "dir.nfo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if state, err := observeNFOCreateTarget(f.directory, "dir.nfo", output); err != nil || state != nfoCreateTargetForeign {
		t.Fatal("directory", state, err)
	}
	// A name below a file cannot be observed: an error, never an interpretation.
	if state, err := observeNFOCreateTarget(f.directory, createTarget+"/below.nfo", output); !errors.Is(err, ErrChanged) || state == nfoCreateTargetAbsent || state == nfoCreateTargetOurs {
		t.Fatal("unobservable name", state, err)
	}
}

func TestPrepareNFOCreateFilesRejectsInvalidArguments(t *testing.T) {
	f := newCreateFixture(t)
	ops := nativeNFOWriteOperations()
	token := commitFileToken()
	persist := f.persistence(0)
	call := func(directory *os.Root, name string, doc *Document, token [16]byte, persist nfoCommitFilePersistence, ops nfoWriteOperations) error {
		_, err := prepareNFOCreateFiles(context.Background(), directory, name, doc, token, persist, ops)
		return err
	}
	noPlan, noReady, noProgress, tooMany := persist, persist, persist, persist
	noPlan.plan, noReady.ready, noProgress.progress = nil, nil, nil
	tooMany.attempt = maxNFOCommitAttempts + 1
	noSync, noDirSync := ops, ops
	noSync.syncFile, noDirSync.syncDirectory = nil, nil
	cases := map[string]error{
		"nil directory":    call(nil, createTarget, f.replacement, token, persist, ops),
		"nil document":     call(f.directory, createTarget, nil, token, persist, ops),
		"zero token":       call(f.directory, createTarget, f.replacement, [16]byte{}, persist, ops),
		"not an nfo":       call(f.directory, "movie.txt", f.replacement, token, persist, ops),
		"path separator":   call(f.directory, "sub/movie.nfo", f.replacement, token, persist, ops),
		"backslash":        call(f.directory, "sub\\movie.nfo", f.replacement, token, persist, ops),
		"colon":            call(f.directory, "movie:stream.nfo", f.replacement, token, persist, ops),
		"control rune":     call(f.directory, "mov\x01ie.nfo", f.replacement, token, persist, ops),
		"no plan hook":     call(f.directory, createTarget, f.replacement, token, noPlan, ops),
		"no ready hook":    call(f.directory, createTarget, f.replacement, token, noReady, ops),
		"no progress hook": call(f.directory, createTarget, f.replacement, token, noProgress, ops),
		"attempt too high": call(f.directory, createTarget, f.replacement, token, tooMany, ops),
		"no file sync":     call(f.directory, createTarget, f.replacement, token, persist, noSync),
		"no dir sync":      call(f.directory, createTarget, f.replacement, token, persist, noDirSync),
	}
	for name, err := range cases {
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatal(name, err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareNFOCreateFiles(cancelled, f.directory, createTarget, f.replacement, token, persist, ops); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled", err)
	}
	if len(f.events) != 0 || len(f.names()) != 0 {
		t.Fatal("an invalid stage had effects", f.events, f.names())
	}
}

func TestPrepareNFOCreateFilesValidatesTheDocumentUnlessTold(t *testing.T) {
	f := newCreateFixture(t)
	broken := &Document{original: []byte("<movie><title>unterminated")}
	if _, err := prepareNFOCreateFiles(context.Background(), f.directory, createTarget, broken, commitFileToken(), f.persistence(0), nativeNFOWriteOperations()); err == nil {
		t.Fatal("an invalid document was staged")
	}
	if len(f.events) != 0 || len(f.names()) != 0 {
		t.Fatal("an invalid document had effects", f.events, f.names())
	}
}

func TestPrepareNFOCreateFilesFailuresLeaveNothingOrOnlyWhatIsRetained(t *testing.T) {
	cases := map[string]struct {
		setup func(f *createFixture, ctx *context.Context, p *nfoCommitFilePersistence, ops *nfoWriteOperations)
		want  func(t *testing.T, f *createFixture, files *nfoCommitFiles, err error)
	}{
		"plan save fails": {
			setup: func(_ *createFixture, _ *context.Context, p *nfoCommitFilePersistence, _ *nfoWriteOperations) {
				p.plan = func(context.Context, nfoCommitFilePlan) error { return domain.ErrDatabase }
			},
			want: func(t *testing.T, f *createFixture, files *nfoCommitFiles, err error) {
				if !errors.Is(err, ErrReplace) || files != nil || len(f.names()) != 0 {
					t.Fatal(err, f.names())
				}
			},
		},
		"output name taken": {
			setup: func(f *createFixture, _ *context.Context, _ *nfoCommitFilePersistence, _ *nfoWriteOperations) {
				name := (nfoCommitFilePlan{token: commitFileToken()}).names()[1]
				if err := os.WriteFile(filepath.Join(f.path, name), []byte("not ours"), 0o600); err != nil {
					f.t.Fatal(err)
				}
			},
			want: func(t *testing.T, f *createFixture, files *nfoCommitFiles, err error) {
				name := (nfoCommitFilePlan{token: commitFileToken()}).names()[1]
				data, readErr := os.ReadFile(filepath.Join(f.path, name))
				if !errors.Is(err, ErrReplace) || files != nil || readErr != nil || string(data) != "not ours" {
					t.Fatal("an object this call did not create was removed or reused", err, readErr)
				}
			},
		},
		"pin name taken": {
			setup: func(f *createFixture, _ *context.Context, _ *nfoCommitFilePersistence, _ *nfoWriteOperations) {
				name := (nfoCommitFilePlan{token: commitFileToken()}).names()[2]
				if err := os.WriteFile(filepath.Join(f.path, name), []byte("not ours"), 0o600); err != nil {
					f.t.Fatal(err)
				}
			},
			want: func(t *testing.T, f *createFixture, files *nfoCommitFiles, err error) {
				if !errors.Is(err, ErrReplace) || files != nil {
					t.Fatal(err)
				}
				// The created output is removed; the foreign pin name stays.
				if names := f.names(); len(names) != 1 {
					t.Fatal("unexpected leftovers", names)
				}
			},
		},
		"file sync fails": {
			setup: func(_ *createFixture, _ *context.Context, _ *nfoCommitFilePersistence, ops *nfoWriteOperations) {
				ops.syncFile = func(*os.File) error { return errors.New("sync") }
			},
			want: func(t *testing.T, f *createFixture, files *nfoCommitFiles, err error) {
				if !errors.Is(err, ErrReplace) || files != nil || len(f.names()) != 0 {
					t.Fatal("a failed sync left files", err, f.names())
				}
			},
		},
		"cancelled while writing": {
			setup: func(_ *createFixture, pctx *context.Context, _ *nfoCommitFilePersistence, ops *nfoWriteOperations) {
				ctx, cancel := context.WithCancel(context.Background())
				*pctx = ctx
				ops.created = cancel
			},
			want: func(t *testing.T, f *createFixture, files *nfoCommitFiles, err error) {
				if !errors.Is(err, context.Canceled) || files != nil || len(f.names()) != 0 || f.targetExists() {
					t.Fatal("a cancelled write left files", err, f.names())
				}
			},
		},
		"directory sync fails": {
			setup: func(_ *createFixture, _ *context.Context, _ *nfoCommitFilePersistence, ops *nfoWriteOperations) {
				ops.syncDirectory = func(*os.Root) error { return errors.New("sync") }
			},
			want: func(t *testing.T, f *createFixture, files *nfoCommitFiles, err error) {
				if !errors.Is(err, ErrReplace) || files != nil || len(f.names()) != 0 || len(f.events) != 1 {
					t.Fatal("a failed directory sync left files or checkpoints", err, f.names(), f.events)
				}
			},
		},
		"checkpoint save fails": {
			setup: func(_ *createFixture, _ *context.Context, p *nfoCommitFilePersistence, _ *nfoWriteOperations) {
				p.progress = func(context.Context, nfoCommitFiles) error { return domain.ErrDatabase }
			},
			want: func(t *testing.T, f *createFixture, files *nfoCommitFiles, err error) {
				// Even an error can follow a committed checkpoint: keep the witnesses.
				if !errors.Is(err, ErrReplace) || files != nil || len(f.names()) != 2 {
					t.Fatal("the witnesses of a possibly committed checkpoint were removed", err, f.names())
				}
			},
		},
		"ready save fails": {
			setup: func(_ *createFixture, _ *context.Context, p *nfoCommitFilePersistence, _ *nfoWriteOperations) {
				p.ready = func(context.Context, nfoCommitFiles) error { return domain.ErrDatabase }
			},
			want: func(t *testing.T, f *createFixture, files *nfoCommitFiles, err error) {
				if !errors.Is(err, ErrReplace) || files == nil || len(f.names()) != 2 {
					t.Fatal("a lost ready response must return the staged files", err, f.names())
				}
			},
		},
		"cancelled after checkpoint": {
			setup: func(_ *createFixture, pctx *context.Context, p *nfoCommitFilePersistence, _ *nfoWriteOperations) {
				ctx, cancel := context.WithCancel(context.Background())
				*pctx = ctx
				p.progress = func(context.Context, nfoCommitFiles) error { cancel(); return nil }
			},
			want: func(t *testing.T, f *createFixture, files *nfoCommitFiles, err error) {
				if !errors.Is(err, context.Canceled) || files != nil || len(f.names()) != 2 {
					t.Fatal(err, f.names())
				}
			},
		},
		"cancelled after ready": {
			setup: func(_ *createFixture, pctx *context.Context, p *nfoCommitFilePersistence, _ *nfoWriteOperations) {
				ctx, cancel := context.WithCancel(context.Background())
				*pctx = ctx
				p.ready = func(context.Context, nfoCommitFiles) error { cancel(); return nil }
			},
			want: func(t *testing.T, f *createFixture, files *nfoCommitFiles, err error) {
				if !errors.Is(err, context.Canceled) || files == nil || len(f.names()) != 2 {
					t.Fatal(err, f.names())
				}
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCreateFixture(t)
			p, ops := f.persistence(0), nativeNFOWriteOperations()
			ctx := context.Background()
			c.setup(f, &ctx, &p, &ops)
			files, err := prepareNFOCreateFiles(ctx, f.directory, createTarget, f.replacement, commitFileToken(), p, ops)
			c.want(t, f, files, err)
			if f.targetExists() {
				t.Fatal("staging must never create the target")
			}
		})
	}
}

func TestPrepareNFOCreateFilesChecksTheSourceAtEveryStep(t *testing.T) {
	stop := errors.New("source changed")
	for call := 1; call <= 3; call++ {
		f := newCreateFixture(t)
		ops := nativeNFOWriteOperations()
		n := 0
		ops.checkSource = func(context.Context) error {
			n++
			if n == call {
				return stop
			}
			return nil
		}
		files, err := f.stage(ops)
		if !errors.Is(err, stop) || files != nil || n != call {
			t.Fatal("check", call, err, n)
		}
		if names := f.names(); len(names) != 0 && call < 3 || f.targetExists() {
			t.Fatal("check", call, "left files", names)
		}
	}
}

func TestPrepareNFOCreateFilesRefusesATargetThatAppearsUnderTheLock(t *testing.T) {
	f := newCreateFixture(t)
	foreign := []byte("<movie><title>raced</title></movie>")
	ops := nativeNFOWriteOperations()
	ops.checkSource = func(context.Context) error {
		// Runs after the plan, before the lock: the second observation finds it.
		return os.WriteFile(filepath.Join(f.path, createTarget), foreign, 0o600)
	}
	files, err := f.stage(ops)
	if !errors.Is(err, ErrTargetAppeared) || files != nil || !bytes.Equal(f.readTarget(), foreign) || len(f.names()) != 1 {
		t.Fatal("a target appearing under the lock was staged against", err, f.names())
	}
}

func TestPrepareNFOCreateFilesResume(t *testing.T) {
	t.Run("retained evidence is reused", func(t *testing.T) {
		f := newCreateFixture(t)
		first := f.mustStage()
		before := f.names()
		resumed := f.persistence(0)
		resumed.resume = first
		f.events = nil
		files, err := prepareNFOCreateFiles(context.Background(), f.directory, createTarget, f.replacement, commitFileToken(), resumed, nativeNFOWriteOperations())
		if err != nil || files == nil || files.output != first.output || !slices.Equal(f.names(), before) {
			t.Fatal("resume did not reuse the retained output", err, f.names())
		}
		if !slices.Equal(f.events, []string{"plan", "checkpoint", "ready"}) {
			t.Fatal("resume must re-save the idempotent evidence", f.events)
		}
	})
	t.Run("a different plan is refused", func(t *testing.T) {
		f := newCreateFixture(t)
		first := *f.mustStage()
		first.plan.filename = "other.nfo"
		resumed := f.persistence(0)
		resumed.resume = &first
		_, err := prepareNFOCreateFiles(context.Background(), f.directory, createTarget, f.replacement, commitFileToken(), resumed, nativeNFOWriteOperations())
		if !errors.Is(err, ErrChanged) {
			t.Fatal("foreign plan", err)
		}
	})
	t.Run("an unsound saved shape is refused", func(t *testing.T) {
		f := newCreateFixture(t)
		first := *f.mustStage()
		first.rollback = first.output
		resumed := f.persistence(0)
		resumed.resume = &first
		_, err := prepareNFOCreateFiles(context.Background(), f.directory, createTarget, f.replacement, commitFileToken(), resumed, nativeNFOWriteOperations())
		if !errors.Is(err, ErrChanged) {
			t.Fatal("unsound evidence", err)
		}
	})
	t.Run("a tampered retained output is refused and kept", func(t *testing.T) {
		f := newCreateFixture(t)
		first := f.mustStage()
		name := first.plan.names()[1]
		if err := os.WriteFile(filepath.Join(f.path, name), []byte("tampered"), 0o600); err != nil {
			t.Fatal(err)
		}
		resumed := f.persistence(0)
		resumed.resume = first
		_, err := prepareNFOCreateFiles(context.Background(), f.directory, createTarget, f.replacement, commitFileToken(), resumed, nativeNFOWriteOperations())
		if err == nil {
			t.Fatal("a tampered output was accepted")
		}
		if data, readErr := os.ReadFile(filepath.Join(f.path, name)); readErr != nil || string(data) != "tampered" {
			t.Fatal("resume removed retained evidence", readErr)
		}
	})
}

func TestVerifyNFOCreateFilesReportsWhatIsWrong(t *testing.T) {
	t.Run("invalid input", func(t *testing.T) {
		f := newCreateFixture(t)
		files := *f.mustStage()
		if verifyNFOCreateFiles(context.Background(), nil, files, f.replacement.original) != ErrInvalidInput ||
			verifyNFOCreateFiles(context.Background(), f.directory, files, nil) != ErrInvalidInput ||
			verifyNFOCreateFiles(context.Background(), f.directory, nfoCommitFiles{}, f.replacement.original) != ErrInvalidInput {
			t.Fatal("invalid input was verified")
		}
		if verifyNFOCreated(context.Background(), nil, files, f.replacement.original) != ErrInvalidInput ||
			verifyNFOCreated(context.Background(), f.directory, files, nil) != ErrInvalidInput ||
			verifyNFOCreated(context.Background(), f.directory, nfoCommitFiles{}, f.replacement.original) != ErrInvalidInput {
			t.Fatal("invalid input was verified after the link")
		}
	})
	t.Run("target present", func(t *testing.T) {
		f := newCreateFixture(t)
		files := *f.mustStage()
		if err := os.WriteFile(filepath.Join(f.path, createTarget), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyNFOCreateFiles(context.Background(), f.directory, files, f.replacement.original); !errors.Is(err, ErrTargetAppeared) {
			t.Fatal(err)
		}
	})
	t.Run("pin removed or changed", func(t *testing.T) {
		f := newCreateFixture(t)
		files := *f.mustStage()
		pin := filepath.Join(f.path, files.plan.names()[2])
		if err := os.Remove(pin); err != nil {
			t.Fatal(err)
		}
		if verifyNFOCreateFiles(context.Background(), f.directory, files, f.replacement.original) == nil {
			t.Fatal("a missing pin verified")
		}
	})
	t.Run("different bytes", func(t *testing.T) {
		f := newCreateFixture(t)
		files := *f.mustStage()
		if verifyNFOCreateFiles(context.Background(), f.directory, files, []byte("<movie/>")) == nil {
			t.Fatal("other bytes verified")
		}
	})
	t.Run("a stray name a create never uses", func(t *testing.T) {
		f := newCreateFixture(t)
		files := *f.mustStage()
		if err := os.WriteFile(filepath.Join(f.path, files.plan.names()[3]), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if verifyNFOCreateFiles(context.Background(), f.directory, files, f.replacement.original) == nil {
			t.Fatal("a stray rollback name verified")
		}
	})
	t.Run("verified after the link", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		if _, _, err := f.settle(files, false, nativeNFOWriteOperations()); err != nil {
			t.Fatal(err)
		}
		if err := verifyNFOCreated(context.Background(), f.directory, *files, f.replacement.original); err != nil {
			t.Fatal(err)
		}
		if verifyNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original) == nil {
			t.Fatal("the pre-link verification must refuse a linked target")
		}
	})
}

func TestVerifyCreateCommitFilesChecksRetainedEvidence(t *testing.T) {
	f := newCreateFixture(t)
	files := f.mustStage()
	token := "01020304-0506-0708-090a-0b0c0d0e0f10"
	plan := domain.NFOWriteCommitFilePlan{Version: domain.NFOWriteCommitCreatePlanVersion, TargetName: createTarget, ParentIdentity: files.plan.parent.record}
	ready := domain.NFOWriteCommitFilesReady{OutputIdentity: files.output.record}
	if err := VerifyCreateCommitFiles(context.Background(), f.directory, token, plan, ready, f.replacement.original); err != nil {
		t.Fatal("retained evidence did not verify", err)
	}
	rewritePlan := plan
	rewritePlan.Version = domain.NFOWriteCommitPlanVersion
	withRollback := ready
	withRollback.RollbackIdentity = ready.OutputIdentity
	for name, err := range map[string]error{
		"nil directory":  VerifyCreateCommitFiles(context.Background(), nil, token, plan, ready, f.replacement.original),
		"bad token":      VerifyCreateCommitFiles(context.Background(), f.directory, "nope", plan, ready, f.replacement.original),
		"rewrite plan":   VerifyCreateCommitFiles(context.Background(), f.directory, token, rewritePlan, ready, f.replacement.original),
		"rollback set":   VerifyCreateCommitFiles(context.Background(), f.directory, token, plan, withRollback, f.replacement.original),
		"empty bytes":    VerifyCreateCommitFiles(context.Background(), f.directory, token, plan, ready, nil),
		"zero plan":      VerifyCreateCommitFiles(context.Background(), f.directory, token, domain.NFOWriteCommitFilePlan{}, ready, f.replacement.original),
		"too many bytes": VerifyCreateCommitFiles(context.Background(), f.directory, token, plan, ready, make([]byte, MaxAllowedBytes+1)),
	} {
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatal(name, err)
		}
	}
	if err := VerifyCreateCommitFiles(context.Background(), f.directory, "a0000000-0000-0000-0000-0000000000aa", plan, ready, f.replacement.original); err == nil {
		t.Fatal("evidence of another token verified")
	}
	if err := VerifyCreateCommitFiles(context.Background(), f.directory, token, plan, ready, []byte("<movie/>")); err == nil {
		t.Fatal("other bytes verified")
	}
}

func TestSettleNFOCreateFilesRejectsInvalidArguments(t *testing.T) {
	f := newCreateFixture(t)
	files := f.mustStage()
	ops := nativeNFOWriteOperations()
	noLink, noSync := ops, ops
	noLink.link, noSync.syncDirectory = nil, nil
	for name, call := range map[string]func() (nfoSettlePhase, error){
		"nil directory": func() (nfoSettlePhase, error) {
			return settleNFOCreateFiles(context.Background(), nil, *files, f.replacement.original, true, nil, ops)
		},
		"no link": func() (nfoSettlePhase, error) {
			return settleNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, true, nil, noLink)
		},
		"no dir sync": func() (nfoSettlePhase, error) {
			return settleNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, true, nil, noSync)
		},
		"unsound files": func() (nfoSettlePhase, error) {
			return settleNFOCreateFiles(context.Background(), f.directory, nfoCommitFiles{}, f.replacement.original, true, nil, ops)
		},
		"no bytes": func() (nfoSettlePhase, error) {
			return settleNFOCreateFiles(context.Background(), f.directory, *files, nil, true, nil, ops)
		},
		"too many bytes": func() (nfoSettlePhase, error) {
			return settleNFOCreateFiles(context.Background(), f.directory, *files, make([]byte, MaxAllowedBytes+1), true, nil, ops)
		},
		"abort nil directory": func() (nfoSettlePhase, error) {
			return abortNFOCreateFiles(context.Background(), nil, *files, f.replacement.original, ops)
		},
		"abort no dir sync": func() (nfoSettlePhase, error) {
			return abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, noSync)
		},
		"abort unsound": func() (nfoSettlePhase, error) {
			return abortNFOCreateFiles(context.Background(), f.directory, nfoCommitFiles{}, f.replacement.original, ops)
		},
		"abort no bytes": func() (nfoSettlePhase, error) {
			return abortNFOCreateFiles(context.Background(), f.directory, *files, nil, ops)
		},
	} {
		if phase, err := call(); !errors.Is(err, ErrInvalidInput) || phase != nfoSettleUnchanged {
			t.Fatal(name, phase, err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if phase, err := settleNFOCreateFiles(cancelled, f.directory, *files, f.replacement.original, true, nil, ops); !errors.Is(err, context.Canceled) || phase != nfoSettleUnchanged {
		t.Fatal("cancelled settle", phase, err)
	}
	if phase, err := abortNFOCreateFiles(cancelled, f.directory, *files, f.replacement.original, ops); !errors.Is(err, context.Canceled) || phase != nfoSettleUnchanged {
		t.Fatal("cancelled abort", phase, err)
	}
	if f.targetExists() {
		t.Fatal("an invalid call created the target")
	}
}

func TestSettleNFOCreateFilesFailuresBeforeTheLinkCreateNothing(t *testing.T) {
	stop := errors.New("source changed")
	t.Run("source checks", func(t *testing.T) {
		for call := 1; call <= 2; call++ {
			f := newCreateFixture(t)
			files := f.mustStage()
			ops := nativeNFOWriteOperations()
			n := 0
			ops.checkSource = func(context.Context) error {
				n++
				if n == call {
					return stop
				}
				return nil
			}
			ops.link = func(*os.Root, string, string) error { t.Fatal("linked after a failed check"); return nil }
			phases, phase, err := f.settle(files, false, ops)
			if !errors.Is(err, stop) || phase != nfoSettleUnchanged || f.targetExists() {
				t.Fatal("check", call, phase, err)
			}
			if call == 2 && !slices.Equal(phases, []nfoSettlePhase{nfoSettleBackedUp}) {
				t.Fatal("phase 1 is durable before the second check", phases)
			}
		}
	})
	t.Run("phase one cannot be saved", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		ops := nativeNFOWriteOperations()
		ops.link = func(*os.Root, string, string) error { t.Fatal("linked without phase 1"); return nil }
		progress := func(context.Context, nfoSettlePhase) error { return domain.ErrDatabase }
		phase, err := settleNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, false, progress, ops)
		if !errors.Is(err, ErrReplace) || phase != nfoSettleUnchanged || f.targetExists() {
			t.Fatal(phase, err)
		}
	})
	t.Run("cancelled after phase one", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		ctx, cancel := context.WithCancel(context.Background())
		ops := nativeNFOWriteOperations()
		ops.link = func(*os.Root, string, string) error { t.Fatal("linked after cancellation"); return nil }
		progress := func(context.Context, nfoSettlePhase) error { cancel(); return nil }
		phase, err := settleNFOCreateFiles(ctx, f.directory, *files, f.replacement.original, false, progress, ops)
		if !errors.Is(err, context.Canceled) || phase != nfoSettleUnchanged || f.targetExists() {
			t.Fatal(phase, err)
		}
	})
	t.Run("staged files damaged", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		if err := os.Remove(filepath.Join(f.path, files.plan.names()[2])); err != nil {
			t.Fatal(err)
		}
		_, phase, err := f.settle(files, false, nativeNFOWriteOperations())
		if err == nil || phase != nfoSettleUnchanged || f.targetExists() {
			t.Fatal("damaged staging was linked", phase, err)
		}
	})
	t.Run("link refused without a trace", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		ops := nativeNFOWriteOperations()
		ops.link = func(*os.Root, string, string) error { return errors.New("link refused") }
		phases, phase, err := f.settle(files, false, ops)
		if !errors.Is(err, ErrReplace) || phase != nfoSettleUnchanged || f.targetExists() || !slices.Equal(phases, []nfoSettlePhase{nfoSettleBackedUp}) {
			t.Fatal(phase, err, phases)
		}
	})
	t.Run("a link error after the link happened is trusted by observation", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		ops := nativeNFOWriteOperations()
		link := ops.link
		ops.link = func(r *os.Root, from, to string) error {
			if err := link(r, from, to); err != nil {
				return err
			}
			return errors.New("lost response")
		}
		phases, phase, err := f.settle(files, false, ops)
		if err != nil || phase != nfoSettleReplaced || !bytes.Equal(f.readTarget(), f.replacement.original) || !slices.Equal(phases, []nfoSettlePhase{nfoSettleBackedUp, nfoSettleReplaced}) {
			t.Fatal(phase, err, phases)
		}
	})
}

func TestSettleNFOCreateFilesFailuresAfterTheLink(t *testing.T) {
	t.Run("replaced checkpoint fails", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		progress := func(_ context.Context, phase nfoSettlePhase) error {
			if phase == nfoSettleReplaced {
				return domain.ErrDatabase
			}
			return nil
		}
		phase, err := settleNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, false, progress, nativeNFOWriteOperations())
		if !errors.Is(err, ErrReplace) || phase != nfoSettleReplaced || !bytes.Equal(f.readTarget(), f.replacement.original) {
			t.Fatal("the created target must stay for the retry", phase, err)
		}
	})
	t.Run("verification fails and the target is provably ours", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		pin := filepath.Join(f.path, files.plan.names()[2])
		ops := nativeNFOWriteOperations()
		sync := ops.syncDirectory
		ops.syncDirectory = func(r *os.Root) error {
			if f.targetExists() {
				if err := os.Remove(pin); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
			return sync(r)
		}
		_, phase, err := f.settle(files, false, ops)
		if !errors.Is(err, ErrRolledBack) || phase != nfoSettleRolledBack || f.targetExists() {
			t.Fatal("an unverifiable link must be undone", phase, err)
		}
	})
	t.Run("verification fails and the target was edited", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		ops := nativeNFOWriteOperations()
		sync := ops.syncDirectory
		edited := false
		ops.syncDirectory = func(r *os.Root) error {
			if f.targetExists() && !edited {
				edited = true
				edit, err := os.OpenFile(filepath.Join(f.path, createTarget), os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := edit.WriteString("<!--edited-->"); err != nil || edit.Close() != nil {
					t.Fatal(err)
				}
			}
			return sync(r)
		}
		_, phase, err := f.settle(files, false, ops)
		if !errors.Is(err, ErrChanged) || phase != nfoSettleIndeterminate || !bytes.HasSuffix(f.readTarget(), []byte("<!--edited-->")) {
			t.Fatal("an edited target must be left alone", phase, err)
		}
	})
	t.Run("rollback cannot be made durable", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		ops := nativeNFOWriteOperations()
		ops.syncDirectory = func(*os.Root) error { return errors.New("sync") }
		_, phase, err := f.settle(files, false, ops)
		if !errors.Is(err, ErrRollback) || phase != nfoSettleIndeterminate {
			t.Fatal(phase, err)
		}
	})
}

func TestSettleNFOCreateFilesResumeFailures(t *testing.T) {
	linked := func(t *testing.T) (*createFixture, *nfoCommitFiles) {
		t.Helper()
		f := newCreateFixture(t)
		files := f.mustStage()
		if _, _, err := f.settle(files, false, nativeNFOWriteOperations()); err != nil {
			t.Fatal(err)
		}
		return f, files
	}
	t.Run("pin missing", func(t *testing.T) {
		f, files := linked(t)
		if err := os.Remove(filepath.Join(f.path, files.plan.names()[2])); err != nil {
			t.Fatal(err)
		}
		_, phase, err := f.settle(files, true, nativeNFOWriteOperations())
		if err == nil || phase != nfoSettleIndeterminate || !bytes.Equal(f.readTarget(), f.replacement.original) {
			t.Fatal(phase, err)
		}
	})
	t.Run("directory sync fails", func(t *testing.T) {
		f, files := linked(t)
		ops := nativeNFOWriteOperations()
		ops.syncDirectory = func(*os.Root) error { return errors.New("sync") }
		phases, phase, err := f.settle(files, true, ops)
		if !errors.Is(err, ErrReplace) || phase != nfoSettleReplaced || len(phases) != 0 {
			t.Fatal(phase, err, phases)
		}
	})
	t.Run("replaced checkpoint fails", func(t *testing.T) {
		f, files := linked(t)
		progress := func(context.Context, nfoSettlePhase) error { return domain.ErrDatabase }
		phase, err := settleNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, true, progress, nativeNFOWriteOperations())
		if !errors.Is(err, ErrReplace) || phase != nfoSettleReplaced {
			t.Fatal(phase, err)
		}
	})
	t.Run("without a progress callback", func(t *testing.T) {
		f, files := linked(t)
		phase, err := settleNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, true, nil, nativeNFOWriteOperations())
		if err != nil || phase != nfoSettleReplaced {
			t.Fatal(phase, err)
		}
	})
}

func TestAbortNFOCreateFilesStates(t *testing.T) {
	t.Run("absent target stays absent", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		phase, err := abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, nativeNFOWriteOperations())
		if err != nil || phase != nfoSettleRolledBack || f.targetExists() {
			t.Fatal(phase, err)
		}
	})
	t.Run("absent target but the directory cannot be synced", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		ops := nativeNFOWriteOperations()
		ops.syncDirectory = func(*os.Root) error { return errors.New("sync") }
		phase, err := abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, ops)
		if !errors.Is(err, ErrRollback) || phase != nfoSettleIndeterminate {
			t.Fatal(phase, err)
		}
	})
	t.Run("foreign target is untouched", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		foreign := []byte("<movie><title>foreign</title></movie>")
		if err := os.WriteFile(filepath.Join(f.path, createTarget), foreign, 0o600); err != nil {
			t.Fatal(err)
		}
		phase, err := abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, nativeNFOWriteOperations())
		if !errors.Is(err, ErrChanged) || phase != nfoSettleIndeterminate || !bytes.Equal(f.readTarget(), foreign) {
			t.Fatal(phase, err)
		}
	})
	t.Run("own object that cannot be unlinked durably", func(t *testing.T) {
		f := newCreateFixture(t)
		files := f.mustStage()
		if _, _, err := f.settle(files, false, nativeNFOWriteOperations()); err != nil {
			t.Fatal(err)
		}
		ops := nativeNFOWriteOperations()
		ops.syncDirectory = func(*os.Root) error { return errors.New("sync") }
		phase, err := abortNFOCreateFiles(context.Background(), f.directory, *files, f.replacement.original, ops)
		if !errors.Is(err, ErrRollback) || phase != nfoSettleIndeterminate {
			t.Fatal(phase, err)
		}
	})
	t.Run("swapped parent", func(t *testing.T) {
		f := newCreateFixture(t)
		files := *f.mustStage()
		other, err := nativeIdentityWithin(lockTestRoot(t, t.TempDir()), ".")
		if err != nil {
			t.Fatal(err)
		}
		files.plan.parent = other
		phase, err := abortNFOCreateFiles(context.Background(), f.directory, files, f.replacement.original, nativeNFOWriteOperations())
		if !errors.Is(err, ErrChanged) || phase != nfoSettleIndeterminate {
			t.Fatal(phase, err)
		}
		phase, err = settleNFOCreateFiles(context.Background(), f.directory, files, f.replacement.original, true, nil, nativeNFOWriteOperations())
		if !errors.Is(err, ErrChanged) || phase != nfoSettleUnchanged || f.targetExists() {
			t.Fatal(phase, err)
		}
	})
}
