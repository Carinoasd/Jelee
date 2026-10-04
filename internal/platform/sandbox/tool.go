package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// ToolHelperCommand dispatches the Matroska, MediaInfo and Tesseract helper.
// It is a separate entry from HelperCommand so the ffprobe descriptor,
// policy and cache identity stay byte-for-byte unchanged.
const ToolHelperCommand = "--internal-media-tool-helper"

// ToolMode is one fixed operation. Each mode has a fixed executable name,
// fixed argv and fixed resource bounds; only extraction carries numeric IDs.
type ToolMode string

// The registered modes: MediaInfo JSON, mkvmerge identification JSON,
// mkvextract extraction into the working directory and Tesseract text
// recognition of one subtitle picture (G15.6).
const (
	ToolMediaInfo ToolMode = "mediainfo"
	ToolIdentify  ToolMode = "mkvmerge-identify"
	ToolExtract   ToolMode = "mkvextract"
	ToolOCR       ToolMode = "tesseract-ocr"
)

// Extraction bounds. Track and attachment IDs come from the tool's own
// identification output, never from a request; output names are derived
// from them ("t<id>", "a<id>") inside the runner's private directory.
const (
	MaxExtractTracks      = 32
	MaxExtractAttachments = 64
	MaxExtractTrackID     = 127
	MaxExtractAttachment  = 4096
	// ExtractFileLimit is RLIMIT_FSIZE for the extraction child: a larger
	// output file stops the tool instead of filling the disk.
	ExtractFileLimit = 64 << 20
)

// OCR bounds. Debian's Tesseract build links libcurl and libarchive with
// their TLS, Kerberos and LDAP stacks, so its exact closure is larger than
// the other tools'. Language data files are read-only grants of their own.
const (
	MaxOCRLibraries = 64
	MaxOCRDataFiles = 8
	MaxOCRLanguages = 4
	// OCRDataSuffix is the only accepted language data file name suffix.
	OCRDataSuffix = ".traineddata"
)

// ToolPolicyVersion must change when tool file grants, syscall permissions,
// argv or resource bounds of the Matroska and MediaInfo modes change. It is
// part of the MediaInfo probe identity, so the OCR mode has its own version.
const ToolPolicyVersion = "linux-media-tool-sandbox-v1"

// OCRPolicyVersion must change when the OCR mode's grants, argv or bounds
// change; it is part of the OCR cache identity.
const OCRPolicyVersion = "linux-ocr-tool-sandbox-v1"

// ToolProfile names the shipped executable for one mode.
type ToolProfile struct {
	Mode ToolMode
	Path string
}

// ToolPolicy comes from shipped trusted identity data; never from the
// descriptor or a request. Libraries must be the exact dependency closure.
type ToolPolicy struct {
	ExecutableSHA256 string
	Libraries        []PinnedFile
	// DataFiles are the OCR mode's language data files, all in one
	// directory and named <language>.traineddata; empty for other modes.
	DataFiles             []PinnedFile
	RequireProtectedFiles bool
}

// Extraction holds the per-run parameters: the identified Matroska track
// and attachment IDs to extract (strictly ascending), or for the OCR mode
// the language codes to recognize, in priority order. Every other field
// stays empty.
type Extraction struct {
	Tracks      []int
	Attachments []int
	Languages   []string
}

type toolDescriptor struct {
	Version     int      `json:"version"`
	Mode        string   `json:"mode"`
	Path        string   `json:"path"`
	Tracks      []int    `json:"tracks,omitempty"`
	Attachments []int    `json:"attachments,omitempty"`
	Languages   []string `json:"languages,omitempty"`
}

// ToolLauncher contains only immutable registration data for one mode.
type ToolLauncher struct {
	executable string
	profile    ToolProfile
	protected  bool
	// languages are the OCR languages whose data the policy pins.
	languages []string
}

var toolNames = map[ToolMode]string{ToolMediaInfo: "mediainfo", ToolIdentify: "mkvmerge", ToolExtract: "mkvextract", ToolOCR: "tesseract"}

func validToolProfile(profile ToolProfile) bool {
	name, ok := toolNames[profile.Mode]
	return ok && validPath(profile.Path) && filepath.Base(profile.Path) == name
}

func validToolPolicy(mode ToolMode, policy ToolPolicy) bool {
	if mode != ToolOCR {
		return len(policy.DataFiles) == 0 && validPolicy(Policy{FFprobeSHA256: policy.ExecutableSHA256, Libraries: policy.Libraries})
	}
	_, ok := dataLanguages(policy.DataFiles)
	return ok && validPolicyWithin(Policy{FFprobeSHA256: policy.ExecutableSHA256, Libraries: policy.Libraries}, MaxOCRLibraries)
}

// validLanguage accepts a Tesseract language code such as eng or chi_tra.
func validLanguage(value string) bool {
	if len(value) < 3 || len(value) > 8 {
		return false
	}
	for index, c := range value {
		if !(c >= 'a' && c <= 'z' || index == 3 && c == '_' && len(value) > 4) {
			return false
		}
	}
	return true
}

// dataLanguages checks the OCR data grants: 1..MaxOCRDataFiles distinct
// <language>.traineddata files in one directory. It returns their codes.
func dataLanguages(files []PinnedFile) ([]string, bool) {
	if len(files) < 1 || len(files) > MaxOCRDataFiles {
		return nil, false
	}
	languages := make([]string, 0, len(files))
	for _, file := range files {
		name := filepath.Base(file.Path)
		code, found := strings.CutSuffix(name, OCRDataSuffix)
		if !validPath(file.Path) || !validDigest(file.SHA256) || !found || !validLanguage(code) || filepath.Dir(file.Path) != filepath.Dir(files[0].Path) || slices.Contains(languages, code) {
			return nil, false
		}
		languages = append(languages, code)
	}
	return languages, true
}

// tessdataDirectory is the one directory of the pinned language data.
func tessdataDirectory(files []PinnedFile) string {
	if len(files) == 0 {
		return ""
	}
	return filepath.Dir(files[0].Path)
}

func ascending(values []int, minimum, maximum, count int) bool {
	if len(values) > count {
		return false
	}
	for index, value := range values {
		if value < minimum || value > maximum || index > 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func validExtraction(mode ToolMode, extraction Extraction) bool {
	if mode == ToolOCR {
		if len(extraction.Tracks)+len(extraction.Attachments) != 0 || len(extraction.Languages) < 1 || len(extraction.Languages) > MaxOCRLanguages {
			return false
		}
		for index, language := range extraction.Languages {
			if !validLanguage(language) || slices.Contains(extraction.Languages[:index], language) {
				return false
			}
		}
		return true
	}
	if len(extraction.Languages) != 0 {
		return false
	}
	if mode != ToolExtract {
		return len(extraction.Tracks) == 0 && len(extraction.Attachments) == 0
	}
	return len(extraction.Tracks)+len(extraction.Attachments) > 0 &&
		ascending(extraction.Tracks, 0, MaxExtractTrackID, MaxExtractTracks) &&
		ascending(extraction.Attachments, 1, MaxExtractAttachment, MaxExtractAttachments)
}

// NewTool validates the approved executable and its exact dependency closure.
// The helper repeats validation immediately before applying policy.
func NewTool(ctx context.Context, profile ToolProfile, policy ToolPolicy) (*ToolLauncher, error) {
	if ctx == nil {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !supportedBuild {
		return nil, ErrUnsupported
	}
	if !validToolProfile(profile) || !validToolPolicy(profile.Mode, policy) {
		return nil, ErrInvalid
	}
	if err := verifyProfile(ctx, Profile{FFprobePath: profile.Path}, toolSandboxPolicy(policy)); err != nil {
		return nil, err
	}
	if err := verifyDataFiles(ctx, policy.DataFiles, policy.RequireProtectedFiles); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, ErrUnavailable
	}
	if policy.RequireProtectedFiles && verifyHelperExecutable(executable) != nil {
		return nil, ErrUnavailable
	}
	languages, _ := dataLanguages(policy.DataFiles)
	return &ToolLauncher{executable: executable, profile: profile, protected: policy.RequireProtectedFiles, languages: languages}, nil
}

func toolSandboxPolicy(policy ToolPolicy) Policy {
	return Policy{FFprobeSHA256: policy.ExecutableSHA256, Libraries: policy.Libraries, RequireProtectedFiles: policy.RequireProtectedFiles}
}

// Mode returns the registered mode.
func (l *ToolLauncher) Mode() ToolMode {
	if l == nil {
		return ""
	}
	return l.profile.Mode
}

// Languages returns the OCR languages whose data the verified policy pins,
// in policy order; nil for other modes.
func (l *ToolLauncher) Languages() []string {
	if l == nil {
		return nil
	}
	return slices.Clone(l.languages)
}

// ProtectedFilesRequired reports the production ownership requirement.
func (l *ToolLauncher) ProtectedFilesRequired() bool { return l != nil && l.protected }

// Executable is the helper binary (this program) that applies the policy.
func (l *ToolLauncher) Executable() string {
	if l == nil {
		return ""
	}
	return l.executable
}

// HelperArguments returns a fresh helper argv for one run. Only extraction
// accepts IDs and only OCR accepts languages, each of which must be pinned
// by the verified policy; every other mode requires an empty Extraction.
func (l *ToolLauncher) HelperArguments(extraction Extraction) ([]string, error) {
	if l == nil || !validExtraction(l.profile.Mode, extraction) {
		return nil, ErrInvalid
	}
	for _, language := range extraction.Languages {
		if !slices.Contains(l.languages, language) {
			return nil, ErrInvalid
		}
	}
	data, err := json.Marshal(toolDescriptor{Version: 1, Mode: string(l.profile.Mode), Path: l.profile.Path, Tracks: extraction.Tracks, Attachments: extraction.Attachments, Languages: extraction.Languages})
	if err != nil || len(data) > MaxDescriptor {
		return nil, ErrInvalid
	}
	return []string{ToolHelperCommand, base64.RawURLEncoding.EncodeToString(data)}, nil
}

// ToolResolver returns the trusted profile and policy for a mode, or false
// when the mode is not registered in this build.
type ToolResolver func(ToolMode) (ToolProfile, ToolPolicy, bool)

// RunToolHelper accepts exactly one encoded descriptor, excluding
// ToolHelperCommand. Call only in a dedicated child and immediately os.Exit
// with the returned code; success replaces the child image.
func RunToolHelper(argv []string, resolve ToolResolver) int {
	if !supportedBuild {
		_, _ = io.WriteString(os.Stderr, "media_sandbox_unavailable\n")
		return ExitUnavailable
	}
	value, err := decodeToolDescriptor(argv)
	if err != nil || resolve == nil {
		_, _ = io.WriteString(os.Stderr, "media_sandbox_invalid\n")
		return ExitInvalid
	}
	profile, policy, ok := resolve(ToolMode(value.Mode))
	if !ok || profile.Path != value.Path || !validToolProfile(profile) || !validToolPolicy(profile.Mode, policy) {
		_, _ = io.WriteString(os.Stderr, "media_sandbox_invalid\n")
		return ExitInvalid
	}
	extraction := Extraction{Tracks: value.Tracks, Attachments: value.Attachments, Languages: value.Languages}
	languages, _ := dataLanguages(policy.DataFiles)
	for _, language := range extraction.Languages {
		if !slices.Contains(languages, language) {
			_, _ = io.WriteString(os.Stderr, "media_sandbox_invalid\n")
			return ExitInvalid
		}
	}
	if executeTool(profile, policy, extraction) != nil {
		_, _ = io.WriteString(os.Stderr, "media_sandbox_unavailable\n")
		return ExitUnavailable
	}
	return ExitUnavailable // A successful exec cannot reach this point.
}

func decodeToolDescriptor(argv []string) (toolDescriptor, error) {
	if len(argv) != 1 || len(argv[0]) > base64.RawURLEncoding.EncodedLen(MaxDescriptor) {
		return toolDescriptor{}, ErrInvalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(argv[0])
	if err != nil || len(data) > MaxDescriptor {
		return toolDescriptor{}, ErrInvalid
	}
	var value toolDescriptor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || value.Version != 1 || decoder.Decode(new(any)) != io.EOF {
		return toolDescriptor{}, ErrInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(data, canonical) {
		return toolDescriptor{}, ErrInvalid
	}
	mode := ToolMode(value.Mode)
	if !validToolProfile(ToolProfile{Mode: mode, Path: value.Path}) || !validExtraction(mode, Extraction{Tracks: value.Tracks, Attachments: value.Attachments, Languages: value.Languages}) {
		return toolDescriptor{}, ErrInvalid
	}
	return value, nil
}

// toolInput is the input path every tool opens. The child receives the
// already verified, read-only regular file as stdin; reopening it through
// the process's own descriptor table never names a library path.
const toolInput = "/proc/self/fd/0"

// toolArguments returns the fixed argv of a mode. Output names are relative
// to the private working directory the runner creates for each run.
func toolArguments(mode ToolMode, extraction Extraction) []string {
	switch mode {
	case ToolMediaInfo:
		return []string{"mediainfo", "--Output=JSON", toolInput}
	case ToolIdentify:
		return []string{"mkvmerge", "--identify", "--identification-format", "json", toolInput}
	case ToolExtract:
		args := []string{"mkvextract", toolInput}
		if len(extraction.Tracks) > 0 {
			args = append(args, "tracks")
			for _, id := range extraction.Tracks {
				args = append(args, strconv.Itoa(id)+":"+ExtractTrackName(id))
			}
		}
		if len(extraction.Attachments) > 0 {
			args = append(args, "attachments")
			for _, id := range extraction.Attachments {
				args = append(args, strconv.Itoa(id)+":"+ExtractAttachmentName(id))
			}
		}
		return append(args, "--quiet")
	}
	return nil
}

// ocrArguments is the fixed Tesseract argv: the verified picture on stdin
// (a PNM file the runner wrote), plain text on stdout, the pinned language
// data directory, the LSTM engine, one uniform block of text and
// Leptonica's tiled Otsu binarization, which on 2026-10-05 removed the empty
// results the global threshold gave for some large outlined CJK lines
// (docs/subtitle-ocr.md). Language codes come from configuration validated
// against the pinned data files.
func ocrArguments(tessdata string, languages []string) []string {
	return []string{"tesseract", toolInput, "stdout", "--tessdata-dir", tessdata, "-l", strings.Join(languages, "+"), "--oem", "1", "--psm", "6", "-c", "thresholding_method=1"}
}

// ExtractTrackName is a track's output file name in the private directory.
func ExtractTrackName(id int) string { return "t" + strconv.Itoa(id) }

// ExtractAttachmentName is an attachment's output file name.
func ExtractAttachmentName(id int) string { return "a" + strconv.Itoa(id) }

// ToolArgumentsDigest describes a mode's fixed argv (without IDs) and the
// policy version, for identity records that must change with them.
func ToolArgumentsDigest(mode ToolMode) string {
	hash := sha256.New()
	version := ToolPolicyVersion
	if mode == ToolOCR {
		version = OCRPolicyVersion
	}
	_, _ = hash.Write([]byte("jelee-media-tool-argv-v1\x00" + version + "\x00"))
	arguments := toolArguments(mode, Extraction{})
	switch mode {
	case ToolExtract:
		arguments = []string{"mkvextract", toolInput, "tracks", "<id>:t<id>", "attachments", "<id>:a<id>", "--quiet"}
	case ToolOCR:
		arguments = ocrArguments("<tessdata>", []string{"<languages>"})
	}
	for _, argument := range slices.Concat([]string{string(mode)}, arguments) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(argument)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(argument))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
