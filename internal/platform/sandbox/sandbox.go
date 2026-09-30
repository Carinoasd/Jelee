// Package sandbox confines a dedicated media helper before it executes ffprobe.
// The helper must run in its own process, before starting the application.
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
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	HelperCommand   = "--internal-probe-helper"
	MaxDescriptor   = 32 << 10
	ExitUnavailable = 78
	ExitInvalid     = 64
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
}

// Launcher contains only immutable registration data; it is not a runner.
type Launcher struct {
	executable string
	descriptor string
	protected  bool
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
	return &Launcher{executable: executable, descriptor: base64.RawURLEncoding.EncodeToString(data), protected: policy.RequireProtectedFiles}, nil
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
	profile, err := decodeDescriptor(argv)
	if err != nil || !validPolicy(policy) {
		_, _ = io.WriteString(os.Stderr, "media_sandbox_invalid\n")
		return ExitInvalid
	}
	err = executeHelper(profile, policy)
	if err != nil {
		_, _ = io.WriteString(os.Stderr, "media_sandbox_unavailable\n")
		return ExitUnavailable
	}
	return ExitUnavailable // A successful exec cannot reach this point.
}

func decodeDescriptor(argv []string) (Profile, error) {
	if len(argv) != 1 || len(argv[0]) > base64.RawURLEncoding.EncodedLen(MaxDescriptor) {
		return Profile{}, ErrInvalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(argv[0])
	if err != nil || len(data) > MaxDescriptor {
		return Profile{}, ErrInvalid
	}
	var value descriptor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || value.Version != 1 || value.Mode != "metadata" {
		return Profile{}, ErrInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Profile{}, ErrInvalid
	}
	// Canonical encoding also rejects duplicate keys and alternate spellings.
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(data, canonical) {
		return Profile{}, ErrInvalid
	}
	profile := Profile{FFprobePath: value.FFprobePath}
	if !validProfile(profile) {
		return Profile{}, ErrInvalid
	}
	return profile, nil
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

func metadataArguments() []string {
	return []string{"-hide_banner", "-v", "error", "-max_alloc", "33554432", "-threads", "1", "-probesize", "1048576", "-analyzeduration", "2000000", "-max_probe_packets", "2500", "-max_streams", "64", "-protocol_whitelist", "fd", "-format_whitelist", "matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,avi,mpegts,mpeg,mpegvideo,flv,ogg", "-enable_drefs", "0", "-show_format", "-show_streams", "-show_chapters", "-of", "json", "-i", "fd:"}
}
