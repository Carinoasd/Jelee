// Package sandbox confines a dedicated media helper before it executes ffprobe.
// The helper must run in its own process, before starting the application.
// It runs one of two fixed operations: the metadata probe and the embedded
// cover read (G40.4), which only dumps the bytes of one stream's first packet.
package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	HelperCommand   = "--internal-probe-helper"
	MaxDescriptor   = 32 << 10
	ExitUnavailable = 78
	ExitInvalid     = 64
	// CoverMaxVideoIndex bounds the video-relative stream index of a cover
	// read. Each index is its own sealed descriptor, never a caller argument.
	CoverMaxVideoIndex = 15
)

var (
	ErrInvalid     = errors.New("media_sandbox_invalid")
	ErrUnavailable = errors.New("media_sandbox_unavailable")
	ErrUnsupported = errors.New("media_sandbox_unsupported")
)

// PinnedFile is an exact canonical file and digest from a trusted dependency
// manifest. Directory grants and automatic trust in host libraries are refused.
type PinnedFile struct {
	Path   string
	SHA256 string
}

// Policy must come from shipped trusted identity data, independently of helper
// argv. Never construct it from the descriptor or from an HTTP request. An empty
// library list only supports an ELF without an interpreter or DT_NEEDED entries.
type Policy struct {
	FFprobeSHA256 string
	Libraries     []PinnedFile
	// RequireProtectedFiles is mandatory for production registration. False
	// permits explicit development fixtures and never implies production safety.
	RequireProtectedFiles bool
}

type Profile struct {
	FFprobePath string
}

type descriptor struct {
	Version     int    `json:"version"`
	Mode        string `json:"mode"`
	FFprobePath string `json:"ffprobePath"`
	// Stream is the video-relative index of a cover read; absent otherwise.
	Stream *int `json:"stream,omitempty"`
}

// Launcher contains only immutable registration data; it is not a runner.
type Launcher struct {
	executable  string
	descriptor  string
	ffprobePath string
	protected   bool
}

// invocation is one decoded helper request: the verified profile and the
// fixed argv selected by its mode. Neither part comes from a caller.
type invocation struct {
	profile   Profile
	arguments []string
}

// New validates the approved executable and exact dependency closure now. The
// helper repeats validation immediately before applying policy and executing.
func New(ctx context.Context, profile Profile, policy Policy) (*Launcher, error) {
	if ctx == nil {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !supportedBuild {
		return nil, ErrUnsupported
	}
	if !validProfile(profile) || !validPolicy(policy) {
		return nil, ErrInvalid
	}
	if err := verifyProfile(ctx, profile, policy); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, ErrUnavailable
	}
	if policy.RequireProtectedFiles && verifyHelperExecutable(executable) != nil {
		return nil, ErrUnavailable
	}
	data, err := json.Marshal(descriptor{Version: 1, Mode: "metadata", FFprobePath: profile.FFprobePath})
	if err != nil || len(data) > MaxDescriptor {
		return nil, ErrInvalid
	}
	return &Launcher{executable: executable, descriptor: base64.RawURLEncoding.EncodeToString(data), ffprobePath: profile.FFprobePath, protected: policy.RequireProtectedFiles}, nil
}

// CoverHelperArguments returns the sealed helper argv of the cover read for
// one video-relative stream index (0..CoverMaxVideoIndex). It reuses the
// executable and dependency closure this launcher verified at construction;
// an index outside the range yields nil.
func (l *Launcher) CoverHelperArguments(stream int) []string {
	if l == nil || l.descriptor == "" || stream < 0 || stream > CoverMaxVideoIndex {
		return nil
	}
	data, err := json.Marshal(descriptor{Version: 1, Mode: "cover", FFprobePath: l.ffprobePath, Stream: &stream})
	if err != nil || len(data) > MaxDescriptor {
		return nil
	}
	return []string{HelperCommand, base64.RawURLEncoding.EncodeToString(data)}
}

func (l *Launcher) ProtectedFilesRequired() bool { return l != nil && l.protected }

func (l *Launcher) Executable() string {
	if l == nil {
		return ""
	}
	return l.executable
}

func (l *Launcher) HelperArguments() []string {
	if l == nil {
		return nil
	}
	return []string{HelperCommand, l.descriptor}
}

// RunHelper accepts exactly one encoded descriptor, excluding HelperCommand.
// Call only in a dedicated child and immediately os.Exit with the returned code.
// Success replaces the child image and never returns. Do not call in a server
// goroutine: the irreversible security policy applies to this process/thread.
func RunHelper(argv []string, policy Policy) int {
	if !supportedBuild {
		_, _ = io.WriteString(os.Stderr, "media_sandbox_unavailable\n")
		return ExitUnavailable
	}
	request, err := decodeDescriptor(argv)
	if err != nil || !validPolicy(policy) {
		_, _ = io.WriteString(os.Stderr, "media_sandbox_invalid\n")
		return ExitInvalid
	}
	err = executeHelper(request.profile, policy, request.arguments)
	if err != nil {
		_, _ = io.WriteString(os.Stderr, "media_sandbox_unavailable\n")
		return ExitUnavailable
	}
	return ExitUnavailable // A successful exec cannot reach this point.
}

func decodeDescriptor(argv []string) (invocation, error) {
	if len(argv) != 1 || len(argv[0]) > base64.RawURLEncoding.EncodedLen(MaxDescriptor) {
		return invocation{}, ErrInvalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(argv[0])
	if err != nil || len(data) > MaxDescriptor {
		return invocation{}, ErrInvalid
	}
	var value descriptor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || value.Version != 1 {
		return invocation{}, ErrInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return invocation{}, ErrInvalid
	}
	// Canonical encoding also rejects duplicate keys and alternate spellings.
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(data, canonical) {
		return invocation{}, ErrInvalid
	}
	var arguments []string
	switch {
	case value.Mode == "metadata" && value.Stream == nil:
		arguments = metadataArguments()
	case value.Mode == "cover" && value.Stream != nil && *value.Stream >= 0 && *value.Stream <= CoverMaxVideoIndex:
		arguments = coverArguments(*value.Stream)
	default:
		return invocation{}, ErrInvalid
	}
	profile := Profile{FFprobePath: value.FFprobePath}
	if !validProfile(profile) {
		return invocation{}, ErrInvalid
	}
	return invocation{profile: profile, arguments: arguments}, nil
}

func validPath(value string) bool {
	return len(value) > 0 && len(value) <= 4096 && utf8.ValidString(value) && filepath.IsAbs(value) && filepath.Clean(value) == value &&
		!strings.ContainsAny(value, ":\x00") && !strings.ContainsFunc(value, unicode.IsControl)
}

func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validProfile(profile Profile) bool {
	return validPath(profile.FFprobePath) && filepath.Base(profile.FFprobePath) == "ffprobe"
}

func validPolicy(policy Policy) bool {
	if !validDigest(policy.FFprobeSHA256) || len(policy.Libraries) > 32 {
		return false
	}
	seen := make(map[string]bool, len(policy.Libraries))
	for _, file := range policy.Libraries {
		if !validPath(file.Path) || !validDigest(file.SHA256) || seen[file.Path] {
			return false
		}
		seen[file.Path] = true
	}
	return true
}

// coverArguments reads one stream's first packet as a hex dump with its
// SHA-256, plus that stream's index, codec, size and attached_pic flag. It
// keeps every input restriction of the metadata probe and decodes nothing:
// the packet payload is the stored picture, byte for byte (G10, G40.4).
func coverArguments(stream int) []string {
	return []string{"-hide_banner", "-v", "error", "-max_alloc", "33554432", "-threads", "1", "-probesize", "1048576", "-analyzeduration", "2000000", "-max_probe_packets", "2500", "-max_streams", "64", "-protocol_whitelist", "fd", "-format_whitelist", "matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,avi,mpegts,mpeg,mpegvideo,flv,ogg", "-enable_drefs", "0",
		"-select_streams", "v:" + strconv.Itoa(stream), "-read_intervals", "%+#1",
		"-show_entries", "stream=index,codec_type,codec_name,width,height:stream_disposition=attached_pic:packet=stream_index,size,data,data_hash",
		"-show_data", "-show_data_hash", "SHA256", "-of", "json", "-i", "fd:"}
}

func metadataArguments() []string {
	return []string{"-hide_banner", "-v", "error", "-max_alloc", "33554432", "-threads", "1", "-probesize", "1048576", "-analyzeduration", "2000000", "-max_probe_packets", "2500", "-max_streams", "64", "-protocol_whitelist", "fd", "-format_whitelist", "matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,avi,mpegts,mpeg,mpegvideo,flv,ogg", "-enable_drefs", "0", "-show_format", "-show_streams", "-show_chapters", "-of", "json", "-i", "fd:"}
}
