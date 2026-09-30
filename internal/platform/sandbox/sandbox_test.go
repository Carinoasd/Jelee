package sandbox

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestDescriptorRejectsOptionsHashesDuplicatesAndUnboundedData(t *testing.T) {
	for name, data := range map[string]string{
		"unknown":   `{"version":1,"mode":"metadata","ffprobePath":"/tmp/ffprobe","extraArgs":["-report"]}`,
		"digest":    `{"version":1,"mode":"metadata","ffprobePath":"/tmp/ffprobe","sha256":"` + strings.Repeat("a", 64) + `"}`,
		"mode":      `{"version":1,"mode":"shell","ffprobePath":"/tmp/ffprobe"}`,
		"version":   `{"version":2,"mode":"metadata","ffprobePath":"/tmp/ffprobe"}`,
		"duplicate": `{"version":1,"mode":"metadata","ffprobePath":"/tmp/ffprobe","mode":"metadata"}`,
		"trailing":  `{"version":1,"mode":"metadata","ffprobePath":"/tmp/ffprobe"}{}`,
		"huge":      strings.Repeat("x", MaxDescriptor+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeDescriptor([]string{base64.RawURLEncoding.EncodeToString([]byte(data))}); err != ErrInvalid {
				t.Fatal("unsafe descriptor accepted")
			}
		})
	}
	for _, args := range [][]string{nil, {"*"}, {"a", "extra"}} {
		if _, err := decodeDescriptor(args); err != ErrInvalid {
			t.Fatal("invalid argument framing accepted")
		}
	}
}

func TestLauncherArgumentsCannotBeMutated(t *testing.T) {
	l := &Launcher{executable: "trusted", descriptor: "fixed"}
	args := l.HelperArguments()
	args[1] = "changed"
	if l.HelperArguments()[1] != "fixed" || l.Executable() != "trusted" {
		t.Fatal("registration was mutable")
	}
	var absent *Launcher
	if absent.Executable() != "" || absent.HelperArguments() != nil {
		t.Fatal("nil launcher exposed executable")
	}
}

func TestPolicyRequiresIndependentDigests(t *testing.T) {
	for _, policy := range []Policy{{}, {FFprobeSHA256: "wrong"}, {FFprobeSHA256: strings.Repeat("A", 64)}, {FFprobeSHA256: strings.Repeat("z", 64)}} {
		if validPolicy(policy) {
			t.Fatal("unverified policy accepted")
		}
	}
	if !validDigest(strings.Repeat("a", 64)) {
		t.Fatal("canonical digest refused")
	}
	data, _ := json.Marshal(descriptor{Version: 1, Mode: "metadata", FFprobePath: "/tmp/ffprobe"})
	if strings.Contains(string(data), "SHA256") || strings.Contains(string(data), "Libraries") || strings.Contains(string(data), "Arguments") {
		t.Fatal("descriptor can define its own trust policy")
	}
}

func TestPolicyMetadataArgumentsIdentityIsStableAndCannotBeMutated(t *testing.T) {
	// Cache identity is a compatibility contract: the domain separator, argument
	// boundaries, order and every option (including fd-only input) must contribute.
	// This golden value was independently calculated from the reviewed argv using
	// SHA-256 and unsigned 64-bit big-endian byte lengths. A deliberate argv change
	// must update this value after reviewing its effect on existing cache entries.
	const expected = "12ba44f191ea78bd2adf7e282522e563552b6ccd32d5ffb4a926a601f7e3927e"
	if got := MetadataArgumentsDigest(); got != expected {
		t.Fatalf("metadata argument identity changed: got %s", got)
	}

	// A caller retaining and modifying an argv slice must not change a future
	// helper invocation or the digest used to decide whether cached data is valid.
	arguments := metadataArguments()
	for i := range arguments {
		arguments[i] = "untrusted-option"
	}
	if got := MetadataArgumentsDigest(); got != expected {
		t.Fatalf("caller mutation changed metadata argument identity: got %s", got)
	}
}
