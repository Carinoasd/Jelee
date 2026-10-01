package proberuntime

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"runtime"
	"slices"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"github.com/MoYuanCN/Jelee/tools"
)

// Identity records embedded executable, library and interpretation pins. It
// performs no filesystem access and does not enable an unavailable runtime.
// Callers must independently create and diagnose the protected runtime; a DB
// identity record never selects an executable, arguments or sandbox policy.
func Identity() (domain.ProbeIdentity, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return domain.ProbeIdentity{}, ErrUnavailable
	}
	return embeddedIdentity()
}

func embeddedIdentity() (domain.ProbeIdentity, error) {
	executable, err := tools.FFprobeSpec("linux-amd64")
	if err != nil {
		return domain.ProbeIdentity{}, ErrUnavailable
	}
	libraries, err := tools.RuntimeSpec("linux-amd64")
	if err != nil {
		return domain.ProbeIdentity{}, ErrUnavailable
	}
	closure, err := runtimeClosureDigest(libraries.Libraries)
	if err != nil {
		return domain.ProbeIdentity{}, ErrUnavailable
	}
	identity := domain.ProbeIdentity{
		Platform: "linux-amd64", VendorVersion: executable.VendorVersion,
		UpstreamVersion: executable.UpstreamVersion, SourceRevision: executable.SourceRevision,
		ExecutableSHA256: executable.SHA256, RuntimeSHA256: closure,
		ParserVersion: domain.ProbeParserVersion, MetadataSchemaVersion: domain.ProbeMetadataSchemaVersion,
		ArgumentsSHA256: sandbox.MetadataArgumentsDigest(), SandboxVersion: sandbox.MetadataPolicyVersion,
		FingerprintVersion: domain.ProbeFingerprintVersion,
	}
	if err := domain.ValidateProbeIdentity(identity); err != nil {
		return domain.ProbeIdentity{}, ErrUnavailable
	}
	return identity, nil
}

func runtimeClosureDigest(files []tools.RuntimeFile) (string, error) {
	if len(files) != 8 {
		return "", ErrUnavailable
	}
	ordered := slices.Clone(files)
	slices.SortFunc(ordered, func(a, b tools.RuntimeFile) int { return strings.Compare(a.ContainerPath, b.ContainerPath) })
	hash := sha256.New()
	_, _ = hash.Write([]byte("jelee-runtime-closure-v1\x00"))
	var previous string
	for _, file := range ordered {
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != 32 || strings.ToLower(file.SHA256) != file.SHA256 ||
			file.ContainerPath == "" || file.ContainerPath == previous ||
			!strings.HasPrefix(file.ContainerPath, "/") || strings.ContainsAny(file.ContainerPath, "\\\x00") {
			return "", ErrUnavailable
		}
		previous = file.ContainerPath
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(file.ContainerPath)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(file.ContainerPath))
		_, _ = hash.Write(digest)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
