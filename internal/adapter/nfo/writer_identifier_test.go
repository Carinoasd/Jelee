//go:build windows || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriterGeneratesMissingIDsInsideSharedOperationAndPreservesOnReplay(t *testing.T) {
	content := []byte(`<root><episode><title>first</title><uniqueid type='custom'>manual</uniqueid></episode><episode><title>second</title><!--keep--><vendor x='1'/></episode></root>`)
	root, name := sourceFixture(t, content)
	source, err := ReadSource(context.Background(), root, name, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	original, err := source.Parse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := original.WithText(context.Background(), 1, "title", "updated", DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	w := &Writer{}
	ops := nativeNFOWriteOperations()
	base := ops
	started := make(chan struct{})
	release := make(chan struct{})
	submitted := make(chan struct{}, 2)
	var once sync.Once
	ops.syncFile = func(file *os.File) error { once.Do(func() { close(started); <-release }); return base.syncFile(file) }
	ops.submitted = func() { submitted <- struct{}{} }
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- w.replace(context.Background(), source, replacement, 1, ops) }()
	<-submitted
	<-started
	go func() { second <- w.replace(context.Background(), source, replacement, 1, ops) }()
	<-submitted
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	parsed := editDocument(t, data)
	if parsed.Entries[0].UniqueIDs[0].Value != "manual" || len(parsed.Entries[1].UniqueIDs) != 1 || parsed.Entries[1].UniqueIDs[0].Type != "jelee" || !bytes.Contains(data, []byte("<!--keep--><vendor x='1'/>")) {
		t.Fatal("IDs or retained data damaged")
	}
	generated := parsed.Entries[1].UniqueIDs[0].Value
	fresh, err := ReadSource(context.Background(), root, name, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	freshDoc, err := fresh.Parse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next, err := freshDoc.WithText(context.Background(), 1, "title", "again", DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Replace(context.Background(), fresh, next, 1); err != nil {
		t.Fatal(err)
	}
	after, err := ReadFile(context.Background(), root, name, DefaultMaxBytes)
	if err != nil || after.Entries[1].UniqueIDs[0].Value != generated {
		t.Fatal("generated ID changed on later write")
	}
	checkNoNFOStages(t, root)
}

func TestWriterFrozenGeneratedIDAndMissingIDLock(t *testing.T) {
	for _, locked := range []bool{false, true} {
		content := `<movie><title>old</title></movie>`
		if locked {
			content = `<movie><title>old</title><lockedfields>ProviderIds</lockedfields></movie>`
		}
		root, name := sourceFixture(t, []byte(content))
		source, err := ReadSource(context.Background(), root, name, DefaultMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := source.Parse(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		changed, err := doc.WithText(context.Background(), 0, "title", "new", DefaultMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		if locked {
			if err := (&Writer{}).Replace(context.Background(), source, changed, 1); err != ErrEditLocked {
				t.Fatalf("ID lock bypassed: %v", err)
			}
			data, _ := os.ReadFile(filepath.Join(root, name))
			if string(data) != content {
				t.Fatal("locked ID write changed source")
			}
		} else {
			changed, err = changed.EnsureIDValue(context.Background(), 0, generatedIDFixture, DefaultMaxBytes, TextEditOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := (&Writer{}).Replace(context.Background(), source, changed, 1); err != nil {
				t.Fatal(err)
			}
			after, err := ReadFile(context.Background(), root, name, DefaultMaxBytes)
			if err != nil || after.Metadata.UniqueIDs[0].Value != generatedIDFixture {
				t.Fatal("frozen ID replaced")
			}
		}
		checkNoNFOStages(t, root)
	}
}
