package probe

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/process"
)

const mediaInfoJSON = `{"creatingLibrary":{"name":"MediaInfoLib","version":"26.05"},
"media":{"@ref":"/proc/self/fd/0","track":[
{"@type":"General","Title":"Jelee synthetic test","Movie":"Jelee synthetic test","Encoded_Application":"Lavf63","File_Modified_Date":"2026-10-04 14:21:16 UTC","FileSize":"42","extra":{"ErrorDetectionType":"Per level 1","Attachments":"Sans.ttf / cover.png","FileExtension_Invalid":"mkv","DIRECTOR":"Somebody","bad key":"x"}},
{"@type":"Video","StreamOrder":"0","Title":"Main"},
{"@type":"Text","StreamOrder":"1","Title":"English SubRip"},
{"@type":"Text","StreamOrder":"9","Title":"Not probed"},
{"@type":"Menu","extra":{"_00_00_00_500":"Part two","_00_00_00_000":"Part one"}},
{"@type":"Menu","extra":{"_00_00_00_100":"Second edition"}}]}}`

func TestParseMediaInfoKeepsOnlyTheWhitelistedSupplement(t *testing.T) {
	duration := int64(1_000_000)
	got := ParseMediaInfo([]byte(mediaInfoJSON), map[int]bool{0: true, 1: true}, &duration)
	if got == nil {
		t.Fatal("supplement dropped")
	}
	if len(got.Chapters) != 2 || got.Chapters[0].Title != "Part one" || got.Chapters[1].StartMicros != 500_000 {
		t.Fatalf("chapters %+v", got.Chapters)
	}
	if len(got.Attachments) != 2 || got.Attachments[0] != (domain.MediaAttachment{ID: 1, FileName: "Sans.ttf", Font: true}) || got.Attachments[1].Font {
		t.Fatalf("attachments %+v", got.Attachments)
	}
	names := []string{}
	for _, tag := range got.Tags {
		names = append(names, tag.Name)
	}
	if strings.Join(names, ",") != "Title,Movie,Encoded_Application,DIRECTOR" {
		t.Fatalf("tags %v: file system fields and invalid names must not be kept", names)
	}
	if len(got.StreamTitles) != 2 || got.StreamTitles[1] != (domain.MediaStreamTitle{Index: 1, Title: "English SubRip"}) {
		t.Fatalf("titles %+v", got.StreamTitles)
	}
	if strings.Contains(fmt.Sprint(got), "/proc") {
		t.Fatal("input path leaked into the supplement")
	}
}

func TestParseMediaInfoDropsUnusableDocuments(t *testing.T) {
	duration := int64(100_000)
	for name, document := range map[string]string{
		"empty":        "",
		"not json":     "{",
		"nothing":      `{"media":{"track":[{"@type":"General"}]}}`,
		"late chapter": `{"media":{"track":[{"@type":"Menu","extra":{"_00_00_00_500":"Too late"}}]}}`,
		"bad minute":   `{"media":{"track":[{"@type":"Menu","extra":{"_00_61_00_000":"x"}}]}}`,
		"oversize":     strings.Repeat(" ", MaxMediaInfoOutput+1),
	} {
		if got := ParseMediaInfo([]byte(document), map[int]bool{0: true}, &duration); got != nil {
			t.Errorf("%s produced %+v", name, got)
		}
	}
	// One unusable attachment name drops the list, never shifts later IDs.
	got := ParseMediaInfo([]byte(`{"media":{"track":[{"@type":"General","Title":"T","extra":{"Attachments":"a.ttf / ../b.ttf"}}]}}`), map[int]bool{}, nil)
	if got == nil || len(got.Attachments) != 0 || len(got.Tags) != 1 {
		t.Fatalf("attachments with an unsafe name %+v", got)
	}
}

type toolExecutorFunc func(context.Context, process.ToolRequest) (process.Result, error)

func (f toolExecutorFunc) Run(ctx context.Context, request process.ToolRequest) (process.Result, error) {
	return f(ctx, request)
}

func matroskaOutput(size int) process.Result {
	return process.Result{Stdout: []byte(fmt.Sprintf(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","width":320,"height":180},{"index":1,"codec_type":"subtitle","codec_name":"subrip"}],"format":{"format_name":"matroska,webm","size":"%d"}}`, size))}
}

func TestAdapterAddsTheSupplementOnlyForMatroska(t *testing.T) {
	source, _, data := adapterFixture(t)
	calls := 0
	supplement := toolExecutorFunc(func(_ context.Context, request process.ToolRequest) (process.Result, error) {
		calls++
		if request.Stdin == nil || request.Collect != nil || len(request.Extraction.Tracks) != 0 {
			t.Fatal("supplement request is not a plain MediaInfo run")
		}
		return process.Result{Stdout: []byte(mediaInfoJSON)}, nil
	})
	output := matroskaOutput(len(data))
	adapter := &Adapter{runner: adapterExecutorFunc(func(context.Context, process.Request) (process.Result, error) { return output, nil }), supplement: supplement, identity: "id"}
	got, err := adapter.Probe(context.Background(), source)
	if err != nil || got.Metadata.Matroska == nil || len(got.Metadata.Matroska.Attachments) != 2 || calls != 1 {
		t.Fatalf("matroska probe: %v %+v", err, got.Metadata.Matroska)
	}
	if _, err := domain.MarshalProbeMetadata(got.Metadata); err != nil {
		t.Fatal("supplemented metadata fails the stored whitelist")
	}
	output = adapterOutput(len(data))
	got, err = adapter.Probe(context.Background(), source)
	if err != nil || got.Metadata.Matroska != nil || calls != 1 {
		t.Fatal("non-Matroska source ran the supplement")
	}
	// A failing MediaInfo keeps the ffprobe result; runtime failures fail it.
	output = matroskaOutput(len(data))
	for err, want := range map[error]error{process.ErrExit: nil, process.ErrOutputLimit: nil, process.ErrBusy: process.ErrBusy, process.ErrTimeout: context.DeadlineExceeded, process.ErrSandboxUnavailable: ErrToolUnavailable} {
		adapter.supplement = toolExecutorFunc(func(context.Context, process.ToolRequest) (process.Result, error) { return process.Result{}, err })
		got, gotErr := adapter.Probe(context.Background(), source)
		if gotErr != want || want == nil && (got.Metadata.Matroska != nil || len(got.Metadata.Streams) != 2) {
			t.Fatalf("%v: %v %+v", err, gotErr, got.Metadata)
		}
	}
	if _, err := NewAdapterWithSupplement(&process.IsolatedRunner{}, nil, "id"); err != ErrInvalidInput {
		t.Fatal("nil supplement accepted")
	}
	if got, err := NewAdapterWithSupplement(&process.IsolatedRunner{}, &process.IsolatedToolRunner{}, "id"); err != nil || got.supplement == nil {
		t.Fatal("valid supplement rejected")
	}
}
