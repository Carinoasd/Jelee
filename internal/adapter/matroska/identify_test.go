package matroska

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/process"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

const testSource = "00000000-0000-4000-8000-000000000001"

const identifyJSON = `{"attachments":[{"content_type":"font/ttf","file_name":"Sans.ttf","id":1,"size":5},{"content_type":"image/png","file_name":"cover.png","id":2,"size":4},{"content_type":"application/octet-stream","file_name":"Serif.OTF","id":3,"size":6}],
"container":{"recognized":true,"supported":true,"type":"Matroska"},
"tracks":[{"id":0,"type":"video","properties":{"codec_id":"V_MPEG4/ISO/AVC"}},{"id":1,"type":"subtitles","properties":{"codec_id":"S_TEXT/UTF8"}},{"id":2,"type":"subtitles","properties":{"codec_id":"S_TEXT/ASS"}},{"id":3,"type":"subtitles","properties":{"codec_id":"S_HDMV/PGS"}},{"id":4,"type":"subtitles","properties":{"codec_id":"D_WEBVTT/SUBTITLES"}}]}`

type fakeTool struct {
	mu       sync.Mutex
	calls    atomic.Int64
	output   string
	err      error
	plans    []sandbox.Extraction
	files    map[string]string
	block    chan struct{}
	onRun    func()
	skipFile string
	// extra names are written in addition to t<id>/a<id> (VobSub pairs).
	extra []string
}

func (f *fakeTool) Run(ctx context.Context, request process.ToolRequest) (process.Result, error) {
	f.calls.Add(1)
	if request.Stdin == nil {
		return process.Result{}, process.ErrInvalid
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return process.Result{}, process.ErrCancelled
		}
	}
	if f.onRun != nil {
		f.onRun()
	}
	if f.err != nil {
		return process.Result{}, f.err
	}
	if request.Collect == nil {
		return process.Result{Stdout: []byte(f.output)}, nil
	}
	f.mu.Lock()
	f.plans = append(f.plans, request.Extraction)
	f.mu.Unlock()
	directory, err := os.MkdirTemp("", "fake-extract-")
	if err != nil {
		return process.Result{}, err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	write := func(name string) {
		if name == f.skipFile {
			return
		}
		content := f.files[name]
		if content == "" {
			content = "content of " + name
		}
		_ = os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600)
	}
	for _, id := range request.Extraction.Tracks {
		write(sandbox.ExtractTrackName(id))
	}
	for _, id := range request.Extraction.Attachments {
		write(sandbox.ExtractAttachmentName(id))
	}
	for _, name := range f.extra {
		write(name)
	}
	if err := request.Collect(directory); err != nil {
		return process.Result{}, err
	}
	return process.Result{}, nil
}

func TestParseIdentifyAcceptsOnlyPositionalMatroskaIdentification(t *testing.T) {
	container, err := ParseIdentify([]byte(identifyJSON))
	if err != nil || len(container.Tracks) != 5 || len(container.Attachments) != 3 {
		t.Fatalf("parse: %v %+v", err, container)
	}
	if format, ok := container.Tracks[1].TextFormat(); !ok || format != "srt" {
		t.Fatal("SubRip track not extractable")
	}
	for _, index := range []int{0, 3, 4} {
		if _, ok := container.Tracks[index].TextFormat(); ok {
			t.Fatalf("track %d offered as text", index)
		}
	}
	if !container.Attachments[0].Font() || container.Attachments[1].Font() || !container.Attachments[2].Font() {
		t.Fatal("font classification")
	}
	for name, document := range map[string]string{
		"empty":       "",
		"not json":    "{",
		"trailing":    identifyJSON + "{}",
		"mp4":         `{"container":{"recognized":true,"supported":true,"type":"QuickTime/MP4"},"tracks":[]}`,
		"unsupported": `{"container":{"recognized":true,"supported":false,"type":"Matroska"},"tracks":[]}`,
		"gap":         `{"container":{"recognized":true,"supported":true,"type":"Matroska"},"tracks":[{"id":1,"type":"video","properties":{}}]}`,
		"attachment":  `{"container":{"recognized":true,"supported":true,"type":"Matroska"},"tracks":[],"attachments":[{"id":2,"file_name":"a.ttf","size":1}]}`,
		"no size":     `{"container":{"recognized":true,"supported":true,"type":"Matroska"},"tracks":[],"attachments":[{"id":1,"file_name":"a.ttf"}]}`,
		"oversize":    strings.Repeat(" ", MaxIdentifyOutput+1),
	} {
		if _, err := ParseIdentify([]byte(document)); err != ErrInvalid {
			t.Errorf("%s accepted", name)
		}
	}
	unsafe := Attachment{FileName: "../evil.ttf", ContentType: "font/ttf", Size: 1}
	if unsafe.Font() {
		t.Fatal("path-like attachment name accepted")
	}
}
