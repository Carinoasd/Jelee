package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNFOValidateOriginalUnchangedAndSummaryPrivate(t *testing.T) {
	t.Setenv("JELEE_DATABASE_URL", "not-a-database")
	root := t.TempDir()
	content := []byte("<?xml version=\"1.0\"?><movie><title>private title</title><!--preserve--><year>2026</year><thumb>private/art.jpg</thumb><extension secret=\"keep\">original</extension></movie>")
	file := filepath.Join(root, "movie.nfo")
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runNFO(context.Background(), []string{"validate", "--root", root, "--file", "movie.nfo"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var result nfoValidation
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.Root != "movie" || result.Entries != 1 || result.OriginalBytes != int64(len(content)) {
		t.Fatalf("unexpected summary: %+v", result)
	}
	for _, secret := range []string{root, "private title", "private/art.jpg", "secret", "not-a-database"} {
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Fatalf("summary contains private data: %q", secret)
		}
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(after, content) {
		t.Fatal("original NFO changed or unavailable")
	}
}

func TestNFOValidateErrorsAndFindings(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name, content string
		code          int
		message       string
	}{
		{"invalid-value", "<movie><title>Movie</title><year>NaN</year></movie>", 3, "nfo_invalid_integer"},
		{"malformed", "<movie>", 1, "nfo_invalid_xml"},
		{"external-entity", "<!DOCTYPE movie SYSTEM 'http://127.0.0.1/private'><movie/>", 1, "nfo_unsafe_xml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := test.name + ".nfo"
			if err := os.WriteFile(filepath.Join(root, file), []byte(test.content), 0600); err != nil {
				t.Fatal(err)
			}
			var out, diagnostics bytes.Buffer
			code := runNFO(context.Background(), []string{"validate", "--root", root, "--file", file}, &out, &diagnostics)
			if code != test.code || !strings.Contains(out.String()+diagnostics.String(), test.message) {
				t.Fatalf("exit=%d out=%s diagnostics=%s", code, &out, &diagnostics)
			}
			if test.code == 1 && out.Len() != 0 {
				t.Fatal("parse failure emitted a successful summary")
			}
			if strings.Contains(out.String()+diagnostics.String(), root) {
				t.Fatal("path disclosed")
			}
		})
	}
}

func TestNFOValidateUnknownRootPrivacy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "private.nfo"), []byte("<private_customer_123><title>Private title</title></private_customer_123>"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if code := runNFO(context.Background(), []string{"validate", "--root", root, "--file", "private.nfo"}, &out, &diagnostics); code != 0 {
		t.Fatalf("exit=%d diagnostics=%s", code, &diagnostics)
	}
	var result nfoValidation
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Root != "unknown" || strings.Contains(out.String()+diagnostics.String(), "private_customer_123") || strings.Contains(out.String(), "Private title") {
		t.Fatalf("unknown root was not sanitized: %s", &out)
	}
	if len(result.Issues) == 0 || result.Issues[0].Severity != "warning" {
		t.Fatal("unknown root warning missing")
	}
}

func TestNFOValidateUsageBoundsAndCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.nfo"), []byte("<movie><title>Movie</title></movie>"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		code int
	}{
		{"empty", nil, 2},
		{"unknown-command", []string{"write"}, 2},
		{"missing-path", []string{"validate"}, 2},
		{"unknown-flag", []string{"validate", "--write"}, 2},
		{"extra-arg", []string{"validate", "--root", root, "--file", "movie.nfo", "extra"}, 2},
		{"negative-limit", []string{"validate", "--root", root, "--file", "movie.nfo", "--max-bytes", "-1"}, 2},
		{"excess-limit", []string{"validate", "--root", root, "--file", "movie.nfo", "--max-bytes", "33554433"}, 2},
		{"size-limit", []string{"validate", "--root", root, "--file", "movie.nfo", "--max-bytes", "4"}, 1},
		{"escape", []string{"validate", "--root", root, "--file", "../outside.nfo"}, 1},
		{"absent", []string{"validate", "--root", root, "--file", "absent.nfo"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			if code := runNFO(context.Background(), test.args, &out, &diagnostics); code != test.code {
				t.Fatalf("exit=%d, want %d", code, test.code)
			}
			if out.Len() != 0 || diagnostics.Len() == 0 {
				t.Fatal("expected only diagnostic output")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, diagnostics bytes.Buffer
	if code := runNFO(ctx, []string{"validate", "--root", root, "--file", "movie.nfo"}, &out, &diagnostics); code != 130 || diagnostics.String() != "nfo_cancelled\n" {
		t.Fatalf("cancellation: exit=%d output=%s", code, &diagnostics)
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("private output path") }

func TestNFOValidateOutputFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.nfo"), []byte("<movie><title>Movie</title></movie>"), 0600); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	if code := runNFO(context.Background(), []string{"validate", "--root", root, "--file", "movie.nfo"}, failedWriter{}, &diagnostics); code != 1 || diagnostics.String() != "nfo_output_failed\n" {
		t.Fatalf("exit=%d diagnostics=%s", code, &diagnostics)
	}
}

type announcedPipe struct {
	*os.File
	started chan struct{}
	once    sync.Once
}

func (p *announcedPipe) Write(data []byte) (int, error) {
	p.once.Do(func() { close(p.started) })
	return p.File.Write(data)
}

func TestNFOValidateCancelsUnreadOutputPipe(t *testing.T) {
	root := t.TempDir()
	content := "<movie><title>Fixture</title>" + strings.Repeat("<year>invalid</year>", 3000) + "</movie>"
	if err := os.WriteFile(filepath.Join(root, "large.nfo"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	output := &announcedPipe{File: writer, started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var diagnostics bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runNFOWithOutputCancellation(ctx, []string{"validate", "--root", root, "--file", "large.nfo"}, output, &diagnostics)
	}()
	select {
	case <-output.started:
	case <-time.After(5 * time.Second):
		t.Fatal("output write did not start")
	}
	cancel()
	select {
	case code := <-done:
		if code != 130 || diagnostics.String() != "nfo_cancelled\n" {
			t.Fatalf("exit=%d diagnostics=%s", code, &diagnostics)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled command remained blocked on unread pipe")
	}
}
