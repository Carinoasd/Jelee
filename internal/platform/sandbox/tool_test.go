package sandbox

import (
	"context"
	"encoding/base64"
	"slices"
	"strconv"
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

func TestOCRDescriptorAndPolicyBounds(t *testing.T) {
	valid := `{"version":1,"mode":"tesseract-ocr","path":"/usr/lib/jelee/tesseract/tesseract","languages":["chi_tra","eng"]}`
	value, err := decodeToolDescriptor(encodeTool(valid))
	if err != nil || !slices.Equal(value.Languages, []string{"chi_tra", "eng"}) {
		t.Fatalf("valid OCR descriptor: %v %+v", err, value)
	}
	for name, data := range map[string]string{
		"no languages":     `{"version":1,"mode":"tesseract-ocr","path":"/usr/lib/jelee/tesseract/tesseract"}`,
		"ids on ocr":       `{"version":1,"mode":"tesseract-ocr","path":"/usr/lib/jelee/tesseract/tesseract","tracks":[1],"languages":["eng"]}`,
		"languages on mkv": `{"version":1,"mode":"mkvmerge-identify","path":"/usr/lib/jelee/mkvtoolnix/mkvmerge","languages":["eng"]}`,
		"duplicate":        `{"version":1,"mode":"tesseract-ocr","path":"/usr/lib/jelee/tesseract/tesseract","languages":["eng","eng"]}`,
		"too many":         `{"version":1,"mode":"tesseract-ocr","path":"/usr/lib/jelee/tesseract/tesseract","languages":["eng","fra","deu","ita","spa"]}`,
		"traversal":        `{"version":1,"mode":"tesseract-ocr","path":"/usr/lib/jelee/tesseract/tesseract","languages":["../eng"]}`,
		"plus":             `{"version":1,"mode":"tesseract-ocr","path":"/usr/lib/jelee/tesseract/tesseract","languages":["eng+jpn"]}`,
		"upper":            `{"version":1,"mode":"tesseract-ocr","path":"/usr/lib/jelee/tesseract/tesseract","languages":["ENG"]}`,
		"name":             `{"version":1,"mode":"tesseract-ocr","path":"/usr/lib/jelee/tesseract/ffprobe","languages":["eng"]}`,
	} {
		if _, err := decodeToolDescriptor(encodeTool(data)); err != ErrInvalid {
			t.Errorf("%s accepted", name)
		}
	}
	for _, code := range []string{"eng", "chi_tra", "chi_sim", "jpn", "osd"} {
		if !validLanguage(code) {
			t.Errorf("%s refused", code)
		}
	}
	for _, code := range []string{"", "en", "e_g", "chi_", "chi_traditional", "eng.", "Eng", "eng\x00"} {
		if validLanguage(code) {
			t.Errorf("%q accepted", code)
		}
	}
	digest := strings.Repeat("b", 64)
	data := func(paths ...string) []PinnedFile {
		var files []PinnedFile
		for _, path := range paths {
			files = append(files, PinnedFile{Path: path, SHA256: digest})
		}
		return files
	}
	if languages, ok := dataLanguages(data("/t/eng.traineddata", "/t/chi_tra.traineddata")); !ok || !slices.Equal(languages, []string{"eng", "chi_tra"}) {
		t.Fatal("valid data grants refused")
	}
	for name, files := range map[string][]PinnedFile{
		"none":       nil,
		"two dirs":   data("/t/eng.traineddata", "/u/jpn.traineddata"),
		"suffix":     data("/t/eng.txt"),
		"duplicate":  data("/t/eng.traineddata", "/t/eng.traineddata"),
		"bad code":   data("/t/x.traineddata"),
		"relative":   data("t/eng.traineddata"),
		"over bound": data("/t/aaa.traineddata", "/t/bbb.traineddata", "/t/ccc.traineddata", "/t/ddd.traineddata", "/t/eee.traineddata", "/t/fff.traineddata", "/t/ggg.traineddata", "/t/hhh.traineddata", "/t/iii.traineddata"),
	} {
		if _, ok := dataLanguages(files); ok {
			t.Errorf("%s data grants accepted", name)
		}
	}
	libraries := func(count int) []PinnedFile {
		var files []PinnedFile
		for i := range count {
			files = append(files, PinnedFile{Path: "/lib/l" + strconv.Itoa(i) + ".so", SHA256: digest})
		}
		return files
	}
	ocr := ToolPolicy{ExecutableSHA256: digest, Libraries: libraries(MaxOCRLibraries), DataFiles: data("/t/eng.traineddata")}
	if !validToolPolicy(ToolOCR, ocr) {
		t.Fatal("OCR closure bound refused")
	}
	ocr.Libraries = libraries(MaxOCRLibraries + 1)
	if validToolPolicy(ToolOCR, ocr) {
		t.Fatal("OCR closure above its bound accepted")
	}
	// The other modes keep the 32-file bound and never take data files.
	if validToolPolicy(ToolIdentify, ToolPolicy{ExecutableSHA256: digest, Libraries: libraries(33)}) ||
		validToolPolicy(ToolIdentify, ToolPolicy{ExecutableSHA256: digest, DataFiles: data("/t/eng.traineddata")}) ||
		validToolPolicy(ToolOCR, ToolPolicy{ExecutableSHA256: digest}) {
		t.Fatal("policy bounds are not per mode")
	}
}

func TestOCRArgumentsAndIdentityAreFixed(t *testing.T) {
	got := ocrArguments("/usr/lib/jelee/tesseract/tessdata", []string{"eng", "chi_tra"})
	if !slices.Equal(got, []string{"tesseract", "/proc/self/fd/0", "stdout", "--tessdata-dir", "/usr/lib/jelee/tesseract/tessdata", "-l", "eng+chi_tra", "--oem", "1", "--psm", "6", "-c", "thresholding_method=1"}) {
		t.Fatalf("ocr argv %q", got)
	}
	if toolArguments(ToolOCR, Extraction{Languages: []string{"eng"}}) != nil {
		t.Fatal("OCR argv must come from the verified policy's data directory")
	}
	digest := ToolArgumentsDigest(ToolOCR)
	for _, mode := range []ToolMode{ToolMediaInfo, ToolIdentify, ToolExtract} {
		if ToolArgumentsDigest(mode) == digest {
			t.Fatal("OCR digest collides")
		}
	}
	// The MediaInfo digest is part of the probe identity; OCR must not move it.
	if ToolPolicyVersion != "linux-media-tool-sandbox-v1" {
		t.Fatal("tool policy version changed")
	}
	var launcher ToolLauncher
	launcher.profile = ToolProfile{Mode: ToolOCR, Path: "/usr/lib/jelee/tesseract/tesseract"}
	launcher.languages = []string{"eng"}
	if _, err := launcher.HelperArguments(Extraction{Languages: []string{"eng"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := launcher.HelperArguments(Extraction{Languages: []string{"jpn"}}); err != ErrInvalid {
		t.Fatal("ungranted language accepted")
	}
	if got := launcher.Languages(); !slices.Equal(got, []string{"eng"}) {
		t.Fatal("languages")
	}
	var absent *ToolLauncher
	if absent.Languages() != nil {
		t.Fatal("nil launcher languages")
	}
}
