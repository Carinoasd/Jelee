//go:build !race && (linux || windows)

package runtime

import (
	"context"
	"errors"
	"github.com/MoYuanCN/Jelee/internal/platform/legacyignore"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFamilyIgnoreServiceNativeHealthAndCleanup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	s, err := newFamilyIgnoreService(context.Background(), true, prepareProductionFamilyIgnore)
	if err != nil || !s.Available() {
		t.Fatal("native helper health failed", err)
	}
	defer s.Close()
	helper, ok := s.backend.(*process.IgnoreRunner)
	if !ok || helper.Stats().Started != 1 || helper.Stats().Active != 0 {
		t.Fatal("readiness did not execute and join helper")
	}
	result, err := s.Evaluate(context.Background(), legacyignore.Batch{Paths: []string{"/a"}})
	if err != nil || len(result.Decisions) != 1 || result.Decisions[0].Kind != legacyignore.BlankExclude {
		t.Fatal("blank source health", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 || helper.Stats().Active != 0 || s.Available() {
		t.Fatal("service leaked helper or temporary files", err)
	}
}

func TestFamilyIgnoreServiceNativeActiveChildCancellation(t *testing.T) {
	for _, closing := range []bool{false, true} {
		name := "reuse-after-cancel"
		if closing {
			name = "close-waits-for-cancel"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("TMPDIR", root)
			t.Setenv("TMP", root)
			t.Setenv("TEMP", root)
			s, err := newFamilyIgnoreService(context.Background(), true, prepareProductionFamilyIgnore)
			if err != nil || !s.Available() {
				t.Fatal("native service readiness", err)
			}
			defer s.Close()
			helper, ok := s.backend.(*process.IgnoreRunner)
			if !ok {
				t.Fatal("native helper missing")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				result, err := s.Evaluate(ctx, legacyignore.Batch{Source: strings.Repeat("(a|aa){1,100}b\n", 4000), Paths: []string{"/" + strings.Repeat("a", 40)}})
				if len(result.Decisions) != 0 {
					done <- errors.New("cancel returned partial decisions")
					return
				}
				done <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			for helper.Stats().Started < 2 || helper.Stats().Active != 1 {
				if time.Now().After(deadline) {
					t.Fatal("native child was not observed active")
				}
				time.Sleep(time.Millisecond)
			}
			closed := make(chan error, 1)
			if closing {
				go func() { closed <- s.Close() }()
				for s.Available() {
					if time.Now().After(deadline) {
						t.Fatal("close did not withdraw capability")
					}
					time.Sleep(time.Millisecond)
				}
				select {
				case err := <-closed:
					t.Fatal("close did not wait for active evaluation", err)
				default:
				}
				if entries, err := os.ReadDir(root); err != nil || len(entries) != 1 {
					t.Fatal("close removed active helper input tree", err)
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, process.ErrCancelled) && !errors.Is(err, context.Canceled) {
					t.Fatal("native cancellation result", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancel did not join child")
			}
			if helper.Stats().Active != 0 {
				t.Fatal("cancel retained child")
			}
			if closing {
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("close did not finish after child join")
				}
			} else {
				if !s.Available() {
					t.Fatal("user cancellation disabled healthy service")
				}
				result, err := s.Evaluate(context.Background(), legacyignore.Batch{Paths: []string{"/a"}})
				if err != nil || len(result.Decisions) != 1 || result.Decisions[0].Kind != legacyignore.BlankExclude {
					t.Fatal("cancelled service not reusable", err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 || s.Available() {
				t.Fatal("closed native service left temporary files", err)
			}
		})
	}
}
