package proberuntime

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/MoYuanCN/Jelee/tools"
)

func TestEmbeddedIdentityMatchesIndependentExecutionPins(t *testing.T) {
	identity, err := embeddedIdentity()
	if err != nil {
		t.Fatal("embedded identity unavailable")
	}
	executable, err := tools.FFprobeSpec("linux-amd64")
	if err != nil || identity.ExecutableSHA256 != executable.SHA256 ||
		identity.ExecutableSHA256 != "a5bd5e9f8d74ab2c6d7d9e2e2738ff6d67bf9613e7143e143ba8ebe9b06385fb" ||
		identity.SourceRevision != executable.SourceRevision || identity.Platform != "linux-amd64" ||
		identity.ArgumentsSHA256 != sandbox.MetadataArgumentsDigest() ||
		identity.SandboxVersion != sandbox.MetadataPolicyVersion ||
		identity.ParserVersion != domain.ProbeParserVersion || identity.FingerprintVersion != domain.ProbeFingerprintVersion {
		t.Fatal("identity does not match fixed interpretation and tool pins")
	}
	if digest, err := domain.ProbeIdentityDigest(identity); err != nil || len(digest) != 64 {
		t.Fatal("identity cannot be registered")
	}
	identity.ExecutableSHA256 = strings.Repeat("b", 64)
	again, _ := embeddedIdentity()
	if again.ExecutableSHA256 == identity.ExecutableSHA256 {
		t.Fatal("caller mutated embedded identity")
	}
	actual, err := Identity()
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		if err != nil || actual != again {
			t.Fatal("Linux identity differs from embedded pins")
		}
	} else if err != ErrUnavailable || actual != (domain.ProbeIdentity{}) {
		t.Fatal("unsupported platform registered production identity")
	}
}

func TestRuntimeClosureIdentityIgnoresOrderAndLocalInstallPaths(t *testing.T) {
	spec, err := tools.RuntimeSpec("linux-amd64")
	if err != nil {
		t.Fatal(err)
	}
	want, err := runtimeClosureDigest(spec.Libraries)
	if err != nil || len(want) != 64 {
		t.Fatal("closure identity unavailable")
	}
	slices.Reverse(spec.Libraries)
	for i := range spec.Libraries {
		spec.Libraries[i].Path = "private-project-installation"
	}
	if got, err := runtimeClosureDigest(spec.Libraries); err != nil || got != want {
		t.Fatal("unordered or local paths affected production identity")
	}
	spec.Libraries[0].SHA256 = strings.Repeat("a", 64)
	if got, err := runtimeClosureDigest(spec.Libraries); err != nil || got == want {
		t.Fatal("changed library identity hit the old key")
	}
	spec.Libraries[0].ContainerPath = "/different-fixed-runtime-path"
	if got, err := runtimeClosureDigest(spec.Libraries); err != nil || got == want {
		t.Fatal("runtime path is missing from identity")
	}
}

func TestRuntimeClosureRejectsMissingDuplicateAndMalformedPins(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "hash", "uppercase", "relative", "nul", "slash", "empty"} {
		t.Run(mode, func(t *testing.T) {
			spec, _ := tools.RuntimeSpec("linux-amd64")
			switch mode {
			case "missing":
				spec.Libraries = spec.Libraries[:7]
			case "duplicate":
				spec.Libraries[0] = spec.Libraries[1]
			case "hash":
				spec.Libraries[0].SHA256 = "invalid"
			case "uppercase":
				spec.Libraries[0].SHA256 = strings.ToUpper(spec.Libraries[0].SHA256)
			case "relative":
				spec.Libraries[0].ContainerPath = "relative"
			case "nul":
				spec.Libraries[0].ContainerPath = "/lib/\x00"
			case "slash":
				spec.Libraries[0].ContainerPath = "/lib\\escape"
			case "empty":
				spec.Libraries[0].ContainerPath = ""
			}
			if digest, err := runtimeClosureDigest(spec.Libraries); err != ErrUnavailable || digest != "" {
				t.Fatal("invalid closure returned an identity")
			}
		})
	}
}
