package postgres

import (
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func probeWindowsIdentity() domain.ProbeIdentity {
	identity := probeTestIdentity()
	identity.Platform = "windows-amd64"
	identity.SandboxVersion = "windows-metadata-sandbox-v1"
	identity.RuntimeSHA256 = strings.Repeat("e", 64)
	return identity
}

// A Windows identity registers next to the Linux one, with its own digest;
// registering it again returns the same row, and the stored platform is read
// back unchanged.
func TestProbeWindowsIdentityRegistersAndRoundTrips(t *testing.T) {
	f := newProbeFixture(t)
	windows := probeWindowsIdentity()
	ref, err := f.s.RegisterProbeIdentity(f.ctx, windows)
	if err != nil || ref.ID == "" || ref.ID == f.identity.ID || ref.Digest == f.identity.Digest {
		t.Fatal("windows identity", ref, err)
	}
	again, err := f.s.RegisterProbeIdentity(f.ctx, windows)
	if err != nil || again != ref {
		t.Fatal("windows identity not idempotent", again, err)
	}
	var platform string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT platform FROM tool_versions WHERE id=$1::uuid`, ref.ID).Scan(&platform); err != nil || platform != "windows-amd64" {
		t.Fatal("stored platform", platform, err)
	}
	// The table still refuses every other platform, whatever the application
	// layer does.
	for _, other := range []string{"darwin-arm64", "linux-arm64", "Windows-amd64", ""} {
		if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO tool_versions(identity_digest,platform,vendor_version,upstream_version,source_revision,executable_sha256,runtime_sha256,parser_version,metadata_schema_version,arguments_sha256,sandbox_version,fingerprint_version) SELECT decode(repeat('7',64),'hex'),$1,vendor_version,upstream_version,source_revision,executable_sha256,runtime_sha256,parser_version,metadata_schema_version,arguments_sha256,sandbox_version,fingerprint_version FROM tool_versions WHERE id=$2::uuid`, other, f.identity.ID); err == nil {
			t.Fatalf("platform %q accepted by the table", other)
		}
	}
}

// The previous schema cannot represent a Windows row: it blocks the
// downgrade. Without one, the migration round-trips, the old constraint is
// back while down, and the Windows platform is accepted again after up.
func TestProbeWindowsPlatformMigrationRefusesAndRoundTrips(t *testing.T) {
	f := newProbeFixture(t)
	if _, err := f.s.RegisterProbeIdentity(f.ctx, probeWindowsIdentity()); err != nil {
		t.Fatal(err)
	}
	refuseRetainedDowngrade(t, f.jobFixture, "probe_windows_platform", "a windows identity row was downgraded")

	g := newProbeFixture(t)
	want := downgradeAboveMigration(t, g.jobFixture, "probe_windows_platform")
	dsn := g.s.Pool.Config().ConnString()
	if version, dirty, err := Migrate(g.ctx, dsn, "down"); err != nil || dirty || version >= want {
		t.Fatal("linux-only schema refused the downgrade", version, dirty, err)
	}
	if _, err := g.s.RegisterProbeIdentity(g.ctx, probeWindowsIdentity()); err == nil {
		t.Fatal("previous schema accepted a windows identity")
	}
	if version, dirty, err := Migrate(g.ctx, dsn, "up"); err != nil || dirty || version != SchemaVersion {
		t.Fatal("upgrade again", version, dirty, err)
	}
	if _, err := g.s.RegisterProbeIdentity(g.ctx, probeWindowsIdentity()); err != nil {
		t.Fatal("windows identity after upgrade", err)
	}
	var linux int
	if err := g.s.Pool.QueryRow(g.ctx, `SELECT count(*) FROM tool_versions WHERE platform='linux-amd64'`).Scan(&linux); err != nil || linux != 1 {
		t.Fatal("round trip lost the linux identity", linux, err)
	}
}
