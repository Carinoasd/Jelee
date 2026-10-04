package sandbox

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

func encodeTool(data string) []string {
	return []string{base64.RawURLEncoding.EncodeToString([]byte(data))}
}

func TestToolDescriptorAcceptsOnlyCanonicalModesAndIDs(t *testing.T) {
	valid := `{"version":1,"mode":"mkvextract","path":"/usr/lib/jelee/mkvtoolnix/mkvextract","tracks":[1,2],"attachments":[1]}`
	value, err := decodeToolDescriptor(encodeTool(valid))
	if err != nil || value.Mode != "mkvextract" || !slices.Equal(value.Tracks, []int{1, 2}) {
		t.Fatalf("valid descriptor: %v %+v", err, value)
	}
	for name, data := range map[string]string{
		"unknown field": `{"version":1,"mode":"mediainfo","path":"/usr/lib/jelee/mediainfo","args":["--Inform=x"]}`,
		"mode":          `{"version":1,"mode":"mkvpropedit","path":"/usr/lib/jelee/mkvtoolnix/mkvpropedit"}`,
		"name":          `{"version":1,"mode":"mediainfo","path":"/usr/lib/jelee/ffprobe"}`,
		"relative":      `{"version":1,"mode":"mediainfo","path":"mediainfo"}`,
		"version":       `{"version":2,"mode":"mediainfo","path":"/usr/lib/jelee/mediainfo"}`,
		"ids on probe":  `{"version":1,"mode":"mediainfo","path":"/usr/lib/jelee/mediainfo","tracks":[1]}`,
		"no ids":        `{"version":1,"mode":"mkvextract","path":"/usr/lib/jelee/mkvtoolnix/mkvextract"}`,
		"unsorted":      `{"version":1,"mode":"mkvextract","path":"/usr/lib/jelee/mkvtoolnix/mkvextract","tracks":[2,1]}`,
		"duplicate id":  `{"version":1,"mode":"mkvextract","path":"/usr/lib/jelee/mkvtoolnix/mkvextract","tracks":[1,1]}`,
		"negative":      `{"version":1,"mode":"mkvextract","path":"/usr/lib/jelee/mkvtoolnix/mkvextract","tracks":[-1]}`,
		"track bound":   `{"version":1,"mode":"mkvextract","path":"/usr/lib/jelee/mkvtoolnix/mkvextract","tracks":[128]}`,
		"attachment 0":  `{"version":1,"mode":"mkvextract","path":"/usr/lib/jelee/mkvtoolnix/mkvextract","attachments":[0]}`,
		"spacing":       `{"version":1, "mode":"mediainfo","path":"/usr/lib/jelee/mediainfo"}`,
		"trailing":      `{"version":1,"mode":"mediainfo","path":"/usr/lib/jelee/mediainfo"}{}`,
		"huge":          strings.Repeat("x", MaxDescriptor+1),
	} {
		if _, err := decodeToolDescriptor(encodeTool(data)); err != ErrInvalid {
			t.Errorf("%s accepted", name)
		}
	}
	for _, args := range [][]string{nil, {"*"}, {"a", "b"}} {
		if _, err := decodeToolDescriptor(args); err != ErrInvalid {
			t.Fatal("invalid framing accepted")
		}
	}
	many := Extraction{}
	for i := range MaxExtractTracks + 1 {
		many.Tracks = append(many.Tracks, i)
	}
	if validExtraction(ToolExtract, many) {
		t.Fatal("track count bound not enforced")
	}
}

func TestToolArgumentsAreFixedPerMode(t *testing.T) {
	if got := toolArguments(ToolMediaInfo, Extraction{}); !slices.Equal(got, []string{"mediainfo", "--Output=JSON", "/proc/self/fd/0"}) {
		t.Fatalf("mediainfo argv %q", got)
	}
	if got := toolArguments(ToolIdentify, Extraction{}); !slices.Equal(got, []string{"mkvmerge", "--identify", "--identification-format", "json", "/proc/self/fd/0"}) {
		t.Fatalf("identify argv %q", got)
	}
	got := toolArguments(ToolExtract, Extraction{Tracks: []int{1, 2}, Attachments: []int{3}})
	if !slices.Equal(got, []string{"mkvextract", "/proc/self/fd/0", "tracks", "1:t1", "2:t2", "attachments", "3:a3", "--quiet"}) {
		t.Fatalf("extract argv %q", got)
	}
	if toolArguments("other", Extraction{}) != nil {
		t.Fatal("unknown mode has argv")
	}
	digests := map[string]bool{}
	for _, mode := range []ToolMode{ToolMediaInfo, ToolIdentify, ToolExtract} {
		digest := ToolArgumentsDigest(mode)
		if len(digest) != 64 || digests[digest] || digest != ToolArgumentsDigest(mode) {
			t.Fatal("argument digests are not stable and distinct")
		}
		digests[digest] = true
	}
}

func TestToolRegistrationRefusesInvalidInputs(t *testing.T) {
	policy := ToolPolicy{ExecutableSHA256: strings.Repeat("a", 64)}
	if _, err := NewTool(nil, ToolProfile{Mode: ToolMediaInfo, Path: "/usr/lib/jelee/mediainfo"}, policy); err != ErrInvalid { //nolint:staticcheck // SA1012: nil context is the case under test
		t.Fatal("nil context accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewTool(cancelled, ToolProfile{Mode: ToolMediaInfo, Path: "/usr/lib/jelee/mediainfo"}, policy); err == nil {
		t.Fatal("cancelled context accepted")
	}
	if !supportedBuild {
		if _, err := NewTool(context.Background(), ToolProfile{Mode: ToolMediaInfo, Path: "/usr/lib/jelee/mediainfo"}, policy); err != ErrUnsupported {
			t.Fatal("unsupported build registered a tool")
		}
		return
	}
	for name, profile := range map[string]ToolProfile{
		"name":     {Mode: ToolMediaInfo, Path: "/usr/lib/jelee/ffprobe"},
		"mode":     {Mode: "mkvpropedit", Path: "/usr/lib/jelee/mkvpropedit"},
		"relative": {Mode: ToolIdentify, Path: "mkvmerge"},
	} {
		if _, err := NewTool(context.Background(), profile, policy); err != ErrInvalid {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := NewTool(context.Background(), ToolProfile{Mode: ToolMediaInfo, Path: "/usr/lib/jelee/mediainfo"}, ToolPolicy{ExecutableSHA256: "short"}); err != ErrInvalid {
		t.Fatal("invalid digest accepted")
	}
	var absent *ToolLauncher
	if absent.Mode() != "" || absent.Executable() != "" || absent.ProtectedFilesRequired() {
		t.Fatal("nil launcher exposed registration")
	}
	if _, err := absent.HelperArguments(Extraction{}); err != ErrInvalid {
		t.Fatal("nil launcher produced arguments")
	}
	if RunToolHelper(encodeTool(`{"version":1,"mode":"mediainfo","path":"/usr/lib/jelee/mediainfo"}`), nil) != ExitInvalid {
		t.Fatal("helper ran without a resolver")
	}
	refuse := func(ToolMode) (ToolProfile, ToolPolicy, bool) { return ToolProfile{}, ToolPolicy{}, false }
	if RunToolHelper(encodeTool(`{"version":1,"mode":"mediainfo","path":"/usr/lib/jelee/mediainfo"}`), refuse) != ExitInvalid {
		t.Fatal("helper ran an unregistered mode")
	}
	other := func(ToolMode) (ToolProfile, ToolPolicy, bool) {
		return ToolProfile{Mode: ToolMediaInfo, Path: "/opt/other/mediainfo"}, policy, true
	}
	if RunToolHelper(encodeTool(`{"version":1,"mode":"mediainfo","path":"/usr/lib/jelee/mediainfo"}`), other) != ExitInvalid {
		t.Fatal("descriptor path differing from the registration accepted")
	}
}
