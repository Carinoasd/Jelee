//go:build windows || darwin || dragonfly || freebsd || linux || netbsd || openbsd

package nfo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestReplaceNFODocumentOneHundredCrossProcessUpdates(t *testing.T) {
	path, dir, original, _ := writeDocumentFixture(t)
	initial, err := original.WithText(context.Background(), 0, "title", "0", DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceNFODocument(context.Background(), dir, "movie.nfo", original, initial, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReplaceNFODocumentProcessHelper$")
			cmd.Env = append(os.Environ(), "JELEE_NFO_WRITE_TEST_DIR="+path)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("cross-process writer: %v %s", err, out)
			}
		}()
	}
	wg.Wait()
	data, err := dir.ReadFile("movie.nfo")
	if err != nil || editDocument(t, data).Metadata.Title != "100" || !bytes.Contains(data, []byte("<!--keep-->")) || !bytes.Contains(data, []byte("<vendor x='1'/>")) {
		t.Fatal("cross-process update lost data")
	}
	checkNoNFOStages(t, path)
}

func TestReplaceNFODocumentProcessHelper(t *testing.T) {
	path := os.Getenv("JELEE_NFO_WRITE_TEST_DIR")
	if path == "" {
		return
	}
	dir := lockTestRoot(t, path)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for count := 0; count < 25; {
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
		original, err := ReadFile(ctx, path, "movie.nfo", DefaultMaxBytes)
		if errors.Is(err, ErrChanged) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		n, err := strconv.Atoi(original.Metadata.Title)
		if err != nil {
			t.Fatal("cross-process title damaged")
		}
		next, err := original.WithText(ctx, 0, "title", strconv.Itoa(n+1), DefaultMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		err = replaceNFODocument(ctx, dir, "movie.nfo", original, next, 0)
		if errors.Is(err, ErrChanged) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
}
