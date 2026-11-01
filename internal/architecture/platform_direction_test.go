package architecture

import (
	"sort"
	"strings"
	"testing"
)

// anyAdapter allows every package under internal/adapter.
const anyAdapter = "*"

// platformAdapterAllowlist lists the only production imports from
// internal/platform/** into internal/adapter/** (G28.2). Adapters build on
// platform services, so the reverse edge is a layering exception and every
// entry carries its reason. Test files are not checked: a platform test may
// drive a real adapter to prove a contract (for example that a processor
// satisfies a telemetry source interface) without linking it into the binary.
// Removing an exception also means removing its entry; stale entries fail.
var platformAdapterAllowlist = map[string]map[string]string{
	"internal/platform/runtime": {
		anyAdapter: "composition root: builds every adapter from configuration and wires it into the Fx lifecycle; depending on adapters is its job",
	},
	"internal/platform/consistency": {
		"internal/adapter/images": "composition fragment shared by the job worker (runtime) and jelee-cli check/repair: opens the image variant prober from the store root",
		"internal/adapter/scan":   "composition fragment shared by the job worker (runtime) and jelee-cli check/repair: supplies the os.Root file prober",
	},
	"internal/platform/setupenv": {
		"internal/adapter/postgres": "NewPostgresSetup is the composition fragment the server and jelee-cli setup share: it reads the store's database facts and the binary's required schema version",
	},
}

// TestPlatformDoesNotDependOnAdapters enforces the adapter -> platform
// direction of G28.2: platform packages may not import adapters except for
// the composition packages in platformAdapterAllowlist. A new reverse edge
// fails here; move the type down to app or domain, or wire it in runtime.
func TestPlatformDoesNotDependOnAdapters(t *testing.T) {
	packages := loadModulePackages(t, repositoryRoot(t))
	used := map[string]bool{}
	platforms := 0
	for _, pkg := range packages {
		if pkg.dir != "internal/platform" && !strings.HasPrefix(pkg.dir, "internal/platform/") {
			continue
		}
		platforms++
		for imported := range pkg.imports {
			adapter, ok := strings.CutPrefix(imported, modulePath+"/")
			if !ok || (adapter != "internal/adapter" && !strings.HasPrefix(adapter, "internal/adapter/")) {
				continue
			}
			allowed := platformAdapterAllowlist[pkg.dir]
			switch {
			case allowed[adapter] != "":
				used[pkg.dir+" -> "+adapter] = true
			case allowed[anyAdapter] != "":
				used[pkg.dir+" -> "+anyAdapter] = true
			default:
				t.Errorf("%s imports %s; platform must not depend on adapters (G28.2). Move the shared type to internal/app or internal/domain, wire it in internal/platform/runtime, or add a reasoned entry to platformAdapterAllowlist", pkg.dir, adapter)
			}
		}
	}
	if platforms < 10 {
		t.Fatalf("found %d platform packages; the scanner is broken", platforms)
	}
	var stale []string
	for from, targets := range platformAdapterAllowlist {
		for target, reason := range targets {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("allowlist entry %s -> %s has no reason", from, target)
			}
			if !used[from+" -> "+target] {
				stale = append(stale, from+" -> "+target)
			}
		}
	}
	sort.Strings(stale)
	for _, edge := range stale {
		t.Errorf("allowlist entry %s is no longer used; remove it", edge)
	}
}
