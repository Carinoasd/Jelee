package mkvruntime

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

func TestRegistrationComesOnlyFromTheEmbeddedManifest(t *testing.T) {
	for _, mode := range Modes {
		profile, policy, err := registration(mode)
		if err != nil || profile.Mode != mode || !strings.HasPrefix(profile.Path, "/usr/lib/jelee/") || !policy.RequireProtectedFiles || len(policy.Libraries) == 0 || len(policy.Libraries) > 32 {
			t.Fatalf("%s: %v %+v", mode, err, profile)
		}
		for _, library := range policy.Libraries {
			if !strings.HasPrefix(library.Path, "/") || len(library.SHA256) != 64 {
				t.Fatalf("%s library %+v", mode, library)
			}
		}
	}
	if _, _, err := registration("mkvpropedit"); err != ErrUnavailable {
		t.Fatal("unregistered mode accepted")
	}
	_, _, err := Registration(sandbox.ToolMediaInfo)
	if (runtime.GOOS == "linux" && runtime.GOARCH == "amd64") != (err == nil) {
		t.Fatal("registration platform gate")
	}
}

func TestSupplementIdentityIsStableAndBoundToTheManifest(t *testing.T) {
	closure, arguments, err := supplementIdentity()
	if err != nil || len(closure) != 64 || arguments != sandbox.ToolArgumentsDigest(sandbox.ToolMediaInfo) {
		t.Fatalf("%v %q %q", err, closure, arguments)
	}
	again, _, _ := supplementIdentity()
	if again != closure {
		t.Fatal("identity is not stable")
	}
	if _, _, err := SupplementIdentity(); (runtime.GOOS == "linux" && runtime.GOARCH == "amd64") != (err == nil) {
		t.Fatal("supplement platform gate")
	}
}

func TestMissingShippedFilesDisableEachMode(t *testing.T) {
	// The development host has no /usr/lib/jelee: every mode reports missing
	// (or unsupported off linux-amd64) and New refuses without executing.
	for _, status := range Diagnose(context.Background()) {
		if status.State != "missing" && status.State != "platform_unsupported" {
			t.Fatalf("%+v", status)
		}
		if status.Version == "" {
			t.Fatalf("%s has no pinned version", status.Name)
		}
	}
	for _, mode := range Modes {
		if _, err := New(context.Background(), mode, t.TempDir()); err != ErrUnavailable {
			t.Fatalf("%s registered without shipped files", mode)
		}
	}
	if Helper([]string{"not-a-descriptor"}) == 0 {
		t.Fatal("helper accepted a malformed descriptor")
	}
	if config(sandbox.ToolExtract, "/tmp").MaxConcurrent != 1 || config(sandbox.ToolMediaInfo, "/tmp").MaxStdoutBytes != 4<<20 {
		t.Fatal("mode limits")
	}
}
