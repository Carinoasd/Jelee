// Package buildinfo reports the server version. Release builds set it at
// link time:
//
//	go build -ldflags "-X github.com/MoYuanCN/Jelee/internal/platform/buildinfo.version=1.2.3" ./cmd/jelee
//
// Without that, or with a value that is not strict semantic versioning, the
// version is DefaultVersion, which matches web/package.json because the web
// client ships in the same image. GET /api/v1/system and the OpenAPI
// info.version both report Version, so plugins' minJeleeVersion compares
// against the server that actually runs.
package buildinfo

import "regexp"

// DefaultVersion is the version of an unstamped build.
const DefaultVersion = "0.1.0"

// version is replaced by the linker (-X); see the package documentation.
var version string

var semanticVersion = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// Valid reports whether v is a strict semantic version of at most 64 bytes.
func Valid(v string) bool { return len(v) <= 64 && semanticVersion.MatchString(v) }

// Version is the stamped version when valid, otherwise DefaultVersion.
func Version() string {
	if Valid(version) {
		return version
	}
	return DefaultVersion
}
