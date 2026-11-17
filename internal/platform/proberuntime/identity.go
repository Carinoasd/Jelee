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
	if runtime.GOARCH != "amd64" || (runtime.GOOS != "linux" && runtime.GOOS != "windows") {
		return domain.ProbeIdentity{}, ErrUnavailable
	}
	return identityFor(runtime.GOOS + "-" + runtime.GOARCH)
}

func embeddedIdentity() (domain.ProbeIdentity, error) { return identityFor("linux-amd64") }

// windowsStaticClosureLabel is hashed as the Windows runtime closure: the
// vendor ffprobe.exe is a static build (ADR 0011), so there are no library
// pins, and the executable digest is carried by its own identity field.
const windowsStaticClosureLabel = "jelee-runtime-closure-windows-static-v1\x00"

// identityFor builds the identity of one platform's fixed probe runtime. Only
// ffprobe is covered: the platform string is part of the digest, so Linux and
// Windows results never share a cache key.
func identityFor(platform string) (domain.ProbeIdentity, error) {
	executable, err := tools.FFprobeSpec(platform)
	if err != nil {
		return domain.ProbeIdentity{}, ErrUnavailable
	}
	var closure, sandboxVersion string
	switch platform {
	case "linux-amd64":
		libraries, err := tools.RuntimeSpec(platform)
		if err != nil {
			return domain.ProbeIdentity{}, ErrUnavailable
		}
		if closure, err = runtimeClosureDigest(libraries.Libraries); err != nil {
			return domain.ProbeIdentity{}, ErrUnavailable
		}
		sandboxVersion = sandbox.MetadataPolicyVersion
	case "windows-amd64":
		sum := sha256.Sum256([]byte(windowsStaticClosureLabel))
		closure, sandboxVersion = hex.EncodeToString(sum[:]), sandbox.WindowsMetadataPolicyVersion
	default:
		return domain.ProbeIdentity{}, ErrUnavailable
	}
	identity := domain.ProbeIdentity{
		Platform: platform, VendorVersion: executable.VendorVersion,
		UpstreamVersion: executable.UpstreamVersion, SourceRevision: executable.SourceRevision,
		ExecutableSHA256: executable.SHA256, RuntimeSHA256: closure,
		ParserVersion: domain.ProbeParserVersion, MetadataSchemaVersion: domain.ProbeMetadataSchemaVersion,
		ArgumentsSHA256: sandbox.MetadataArgumentsDigest(), SandboxVersion: sandboxVersion,
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

// WithSupplement extends an identity with the MediaInfo supplement
// (G19.1): the supplement's executable/closure digest folds into
// RuntimeSHA256 and its argv digest into ArgumentsSHA256, so results cached
// with and without the supplement never share an identity.
func WithSupplement(identity domain.ProbeIdentity, closure, arguments string) (domain.ProbeIdentity, error) {
	fold := func(label, base, extra string) (string, error) {
		a, errA := hex.DecodeString(base)
		b, errB := hex.DecodeString(extra)
		if errA != nil || errB != nil || len(a) != 32 || len(b) != 32 {
			return "", ErrUnavailable
		}
		hash := sha256.New()
		_, _ = hash.Write([]byte(label))
		_, _ = hash.Write(a)
		_, _ = hash.Write(b)
		return hex.EncodeToString(hash.Sum(nil)), nil
	}
	var err error
	if identity.RuntimeSHA256, err = fold("jelee-runtime-closure-mediainfo-v1\x00", identity.RuntimeSHA256, closure); err != nil {
		return domain.ProbeIdentity{}, err
	}
	if identity.ArgumentsSHA256, err = fold("jelee-probe-arguments-mediainfo-v1\x00", identity.ArgumentsSHA256, arguments); err != nil {
		return domain.ProbeIdentity{}, err
	}
	if domain.ValidateProbeIdentity(identity) != nil {
		return domain.ProbeIdentity{}, ErrUnavailable
	}
	return identity, nil
}
