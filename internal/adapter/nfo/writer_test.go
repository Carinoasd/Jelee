//go:build windows || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func boundWriterFixture(t *testing.T) (string, *Source, *Document) {
	t.Helper()
	root, _ := sourceFixture(t, []byte(`<movie><title>old</title><uniqueid type='imdb'>manual</uniqueid><!--keep--><vendor x='1'/></movie>`))
	name := "電影 title.nfo"
	source, err := ReadSource(context.Background(), root, name, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	document, err := source.Parse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := document.WithText(context.Background(), 0, "title", "new", DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return root, source, replacement
}

func TestWriterBoundEditsChainAndRejectArbitraryReplacement(t *testing.T) {
	root, source, replacement := boundWriterFixture(t)
	w := &Writer{}
	for _, content := range []string{`<movie><title>new</title><uniqueid type='imdb'>overwritten</uniqueid></movie>`, `<movie><title>different original</title></movie>`} {
		untrusted := editDocument(t, []byte(content))
		if err := w.Replace(context.Background(), source, untrusted, 1); err != ErrInvalidInput {
			t.Fatal("arbitrary XML accepted")
		}
		untrusted, err := untrusted.WithText(context.Background(), 0, "title", "new", DefaultMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Replace(context.Background(), source, untrusted, 1); err != ErrInvalidInput {
			t.Fatal("different edit lineage accepted")
		}
	}
	replacement, err := replacement.WithTextOptions(context.Background(), 0, "plot", "added", DefaultMaxBytes, TextEditOptions{CreateMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Replace(context.Background(), source, replacement, 1); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, source.relative))
	if !bytes.Equal(data, replacement.original) || !bytes.Contains(data, []byte("manual")) || !bytes.Contains(data, []byte("<!--keep-->")) {
		t.Fatal("bound edit damaged retained data")
	}
	if err := w.Replace(context.Background(), source, replacement, 1); err != ErrChanged {
		t.Fatal("stale source replay accepted")
	}
	for _, rendered := range []string{fmt.Sprintf("%+v", source), fmt.Sprintf("%#v", source), fmt.Sprintf("%+v", w)} {
		if strings.Contains(rendered, root) || strings.Contains(rendered, "manual") {
			t.Fatal("diagnostic leaked private source")
		}
	}
}

func TestWriterRejectsPhysicalReplacementsWithEqualBytesAndTime(t *testing.T) {
	for _, changed := range []string{"file", "parent", "root"} {
		t.Run(changed, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "library")
			parent := filepath.Join(root, "parent")
			if err := os.MkdirAll(parent, 0700); err != nil {
				t.Fatal(err)
			}
			data := []byte(`<movie><title>old</title></movie>`)
			file := filepath.Join(parent, "movie.nfo")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			source, err := ReadSource(context.Background(), root, "parent/movie.nfo", DefaultMaxBytes)
			if err != nil {
				t.Fatal(err)
			}
			original, _ := source.Parse(context.Background())
			replacement, _ := original.WithText(context.Background(), 0, "title", "new", DefaultMaxBytes)
			before, _ := os.Stat(file)
			switch changed {
			case "file":
				if err := os.Rename(file, file+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(file, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "parent":
				if err := os.Rename(parent, parent+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(parent+".old", "movie.nfo"), file); err != nil {
					t.Fatal(err)
				}
			case "root":
				if err := os.Rename(root, root+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(parent, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(root+".old", "parent", "movie.nfo"), file); err != nil {
					t.Fatal(err)
				}
			}
			if err := (&Writer{}).Replace(context.Background(), source, replacement, 1); err != ErrChanged {
				t.Fatalf("physical replacement accepted: %v", err)
			}
			actual, _ := os.ReadFile(file)
			if !bytes.Equal(actual, data) {
				t.Fatal("replacement mutated")
			}
		})
	}
}

func TestWriterSingleflightIntentAndCancellation(t *testing.T) {
	for _, scenario := range []string{"identical", "fresh_identical", "different_text", "different_backups", "cancel_waiter", "cancel_owner"} {
		t.Run(scenario, func(t *testing.T) {
			root, source, replacement := boundWriterFixture(t)
			w := &Writer{}
			ops := nativeNFOWriteOperations()
			base := ops
			started := make(chan struct{})
			release := make(chan struct{})
			submitted := make(chan struct{}, 2)
			var once sync.Once
			var renamed atomic.Int64
			ops.syncFile = func(file *os.File) error { once.Do(func() { close(started); <-release }); return base.syncFile(file) }
			ops.rename = func(dir *os.Root, from, to string) error {
				if to == source.relative {
					renamed.Add(1)
				}
				return base.rename(dir, from, to)
			}
			ops.submitted = func() { submitted <- struct{}{} }
			ownerCtx, cancelOwner := context.WithCancel(context.Background())
			defer cancelOwner()
			waiterCtx, cancelWaiter := context.WithCancel(context.Background())
			defer cancelWaiter()
			owner := make(chan error, 1)
			go func() { owner <- w.replace(ownerCtx, source, replacement, 1, ops) }()
			<-submitted
			<-started
			second := replacement
			secondSource := source
			if scenario == "fresh_identical" {
				fresh, err := ReadSource(context.Background(), root, source.relative, DefaultMaxBytes)
				if err != nil {
					t.Fatal(err)
				}
				secondSource = fresh
			}
			backups := 1
			if scenario == "different_text" {
				original, _ := source.Parse(context.Background())
				second, _ = original.WithText(context.Background(), 0, "title", "other", DefaultMaxBytes)
			}
			if scenario == "different_backups" {
				backups = 2
			}
			waiter := make(chan error, 1)
			go func() { waiter <- w.replace(waiterCtx, secondSource, second, backups, ops) }()
			<-submitted
			if scenario == "cancel_waiter" {
				cancelWaiter()
				select {
				case err := <-waiter:
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("waiter cancellation blocked")
				}
			}
			if scenario == "cancel_owner" {
				cancelOwner()
			}
			close(release)
			ownerErr := <-owner
			if scenario == "cancel_owner" {
				if !errors.Is(ownerErr, context.Canceled) {
					t.Fatal(ownerErr)
				}
			} else if ownerErr != nil {
				t.Fatal(ownerErr)
			}
			if scenario != "cancel_waiter" {
				err := <-waiter
				switch scenario {
				case "different_text", "different_backups":
					if err != ErrChanged {
						t.Fatalf("different intent merged: %v", err)
					}
				case "cancel_owner":
					if !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				default:
					if err != nil {
						t.Fatalf("identical intent not merged: %v", err)
					}
				}
			}
			want := int64(1)
			if scenario == "cancel_owner" {
				want = 0
			}
			if renamed.Load() != want {
				t.Fatal("unexpected physical writes")
			}
			checkNoNFOStages(t, root)
			w.mu.Lock()
			active := len(w.intents)
			w.mu.Unlock()
			if active != 0 {
				t.Fatal("completed physical intent retained")
			}
		})
	}
}

func TestWriterDoesNotShareReplacedPhysicalSourceIntent(t *testing.T) {
	root, source, replacement := boundWriterFixture(t)
	w := &Writer{}
	ops := nativeNFOWriteOperations()
	base := ops
	started := make(chan struct{})
	release := make(chan struct{})
	submitted := make(chan struct{}, 2)
	var once sync.Once
	ops.syncFile = func(file *os.File) error { once.Do(func() { close(started); <-release }); return base.syncFile(file) }
	ops.submitted = func() { submitted <- struct{}{} }
	first := make(chan error, 1)
	go func() { first <- w.replace(context.Background(), source, replacement, 1, ops) }()
	<-submitted
	<-started
	file := filepath.Join(root, source.relative)
	if err := os.Rename(file, file+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, source.original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, source.fileInfo.ModTime(), source.fileInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	fresh, err := ReadSource(context.Background(), root, source.relative, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if source.Stamp() != fresh.Stamp() {
		t.Fatal("fixture stamp differs")
	}
	second := make(chan error, 1)
	go func() { second <- w.replace(context.Background(), fresh, replacement, 1, ops) }()
	<-submitted
	close(release)
	if err := <-first; err != ErrChanged {
		t.Fatalf("old physical source accepted: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("new physical source joined old failure: %v", err)
	}
	data, _ := os.ReadFile(file)
	if !bytes.Equal(data, replacement.original) {
		t.Fatal("new source not written")
	}
	checkNoNFOStages(t, root)
}
