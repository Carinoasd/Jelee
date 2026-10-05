//go:build windows || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
)

type recordingWriterBudget struct {
	*resources.Budget
	mu      sync.Mutex
	classes []app.WorkClass
}

func (b *recordingWriterBudget) Acquire(ctx context.Context, class app.WorkClass) (func(), error) {
	b.mu.Lock()
	b.classes = append(b.classes, class)
	b.mu.Unlock()
	return b.Budget.Acquire(ctx, class)
}

func writerBudget(t *testing.T, total, queue int) *resources.Budget {
	t.Helper()
	b, err := resources.New(resources.Limits{CPU: 1, IO: 1, Total: total, Queue: queue})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWriterBudgetStagesTotalOneAndFailureCleanup(t *testing.T) {
	if _, err := NewWriterWithBudget(nil); err != domain.ErrInvalid {
		t.Fatal("nil budget accepted")
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "sync failure"}[fail], func(t *testing.T) {
			root, source, replacement := boundWriterFixture(t)
			b := &recordingWriterBudget{Budget: writerBudget(t, 1, 0)}
			w, _ := NewWriterWithBudget(b)
			ops := nativeNFOWriteOperations()
			syncFile := ops.syncFile
			ops.syncFile = func(file *os.File) error {
				if got := b.Stats(); got != (resources.Stats{IO: 1, Total: 1}) {
					t.Errorf("file sync outside exclusive IO stage: %+v", got)
				}
				if fail {
					return errors.New("injected sync failure")
				}
				return syncFile(file)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := w.replace(ctx, source, replacement, 1, ops)
			if fail && err != ErrReplace || !fail && err != nil {
				t.Fatal(err)
			}
			if got := b.Stats(); got != (resources.Stats{}) {
				t.Fatalf("permit leaked: %+v", got)
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			if len(b.classes) != 2 || b.classes[0] != app.WorkCPU || b.classes[1] != app.WorkIO {
				t.Fatal("CPU/IO stages not separated")
			}
			data, err := os.ReadFile(filepath.Join(root, source.relative))
			if err != nil {
				t.Fatal(err)
			}
			want := replacement.original
			if fail {
				want = source.original
			}
			if !bytes.Equal(data, want) {
				t.Fatal("unexpected file bytes")
			}
		})
	}
}

func TestWriterBudgetBusyBeforeFilesystemEffects(t *testing.T) {
	for _, class := range []app.WorkClass{app.WorkCPU, app.WorkIO} {
		t.Run(map[app.WorkClass]string{app.WorkCPU: "cpu", app.WorkIO: "io"}[class], func(t *testing.T) {
			root, source, replacement := boundWriterFixture(t)
			b := writerBudget(t, 2, 0)
			release, _ := b.Acquire(context.Background(), class)
			defer release()
			w, _ := NewWriterWithBudget(b)
			if err := w.Replace(context.Background(), source, replacement, 1); err != domain.ErrResourceBusy {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatal("busy request created filesystem artifacts")
			}
			data, _ := os.ReadFile(filepath.Join(root, source.relative))
			if !bytes.Equal(data, source.original) {
				t.Fatal("busy request changed source")
			}
			release()
			if got := b.Stats(); got != (resources.Stats{}) {
				t.Fatalf("permit leaked: %+v", got)
			}
			if err := w.Replace(context.Background(), source, replacement, 1); err != nil {
				t.Fatal("retry failed", err)
			}
		})
	}
}

func TestWriterBudgetQueuedCancellationRecoversBothStages(t *testing.T) {
	for _, class := range []app.WorkClass{app.WorkCPU, app.WorkIO} {
		t.Run(map[app.WorkClass]string{app.WorkCPU: "cpu", app.WorkIO: "io"}[class], func(t *testing.T) {
			root, source, replacement := boundWriterFixture(t)
			b := writerBudget(t, 2, 1)
			release, _ := b.Acquire(context.Background(), class)
			defer release()
			w, _ := NewWriterWithBudget(b)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- w.Replace(ctx, source, replacement, 1) }()
			deadline := time.Now().Add(5 * time.Second)
			for b.Stats().Waiting != 1 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if b.Stats().Waiting != 1 {
				t.Fatal("request did not enter queue")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancel did not join")
			}
			release()
			if got := b.Stats(); got != (resources.Stats{}) {
				t.Fatalf("queue or permit leaked: %+v", got)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatal("cancelled queue created filesystem artifacts")
			}
			if err := w.Replace(context.Background(), source, replacement, 1); err != nil {
				t.Fatal("retry failed", err)
			}
		})
	}
}

func TestWriterBudgetSharedWaiterDoesNotReleaseOwnerIO(t *testing.T) {
	_, source, replacement := boundWriterFixture(t)
	b := writerBudget(t, 1, 0)
	w, _ := NewWriterWithBudget(b)
	entered, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(gate) })
	ops := nativeNFOWriteOperations()
	syncFile := ops.syncFile
	var first sync.Once
	ops.syncFile = func(file *os.File) error { first.Do(func() { close(entered); <-gate }); return syncFile(file) }
	owner := make(chan error, 1)
	go func() { owner <- w.replace(context.Background(), source, replacement, 1, ops) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not enter IO")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	submitted := make(chan struct{})
	waitOps := nativeNFOWriteOperations()
	waitOps.submitted = func() { close(submitted) }
	waiter := make(chan error, 1)
	go func() { waiter <- w.replace(ctx, source, replacement, 1, waitOps) }()
	<-submitted
	cancel()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter stuck")
	}
	if got := b.Stats(); got != (resources.Stats{IO: 1, Total: 1}) {
		t.Fatalf("waiter released owner IO: %+v", got)
	}
	once.Do(func() { close(gate) })
	select {
	case err := <-owner:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owner stuck")
	}
	if got := b.Stats(); got != (resources.Stats{}) {
		t.Fatalf("owner permit leaked: %+v", got)
	}
}
