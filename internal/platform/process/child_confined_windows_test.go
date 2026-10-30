//go:build windows

package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// These tests need a real Windows 10 1607+ host (owner verification queue
// B18). On Linux they are only compiled (GOOS=windows go test -c).

type tokenReport struct {
	Integrity  string
	AdminAllow bool
	Privileges int
}

func TestConfinedProcessHelper(_ *testing.T) {
	index := -1
	for i, argument := range os.Args {
		if argument == "--confined-process-test" {
			index = i
			break
		}
	}
	if index < 0 || index+1 >= len(os.Args) {
		return
	}
	args := os.Args[index+1:]
	switch args[0] {
	case "token":
		var token windows.Token
		if windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token) != nil {
			os.Exit(20)
		}
		report := tokenReport{}
		if buffer, err := tokenInformation(token, windows.TokenIntegrityLevel); err == nil {
			report.Integrity = (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buffer[0])).Label.Sid.String() //nolint:gosec // G103: TOKEN_MANDATORY_LABEL written by the API
		}
		if groups, err := token.GetTokenGroups(); err == nil {
			for _, group := range groups.AllGroups() {
				if group.Sid.String() == "S-1-5-32-544" && group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 {
					report.AdminAllow = true
				}
			}
		}
		if buffer, err := tokenInformation(token, windows.TokenPrivileges); err == nil {
			report.Privileges = len((*windows.Tokenprivileges)(unsafe.Pointer(&buffer[0])).AllPrivileges()) //nolint:gosec // G103: TOKEN_PRIVILEGES written by the API
		}
		_ = json.NewEncoder(os.Stdout).Encode(report)
	case "write":
		// Exit 0 only when No-Write-Up held for every target.
		for _, target := range append([]string{"t0"}, args[1:]...) {
			if os.WriteFile(target, []byte("escaped"), 0600) == nil {
				os.Exit(21)
			}
		}
	case "spawn":
		executable, _ := os.Executable()
		if exec.Command(executable, "-test.run=^$").Start() == nil {
			os.Exit(22)
		}
	case "stdin":
		data := make([]byte, 5)
		if n, _ := os.Stdin.Read(data); n != 5 || string(data) != "media" {
			os.Exit(23)
		}
	case "allocate":
		memory := make([]byte, 3<<30)
		for i := 0; i < len(memory); i += 4096 {
			memory[i] = 1
		}
	case "sleep":
		time.Sleep(30 * time.Second)
	default:
		os.Exit(24)
	}
	os.Exit(0)
}

func confinedArgs(args ...string) []string {
	return append([]string{"-test.run=^TestConfinedProcessHelper$", "--", "--confined-process-test"}, args...)
}

func confinedRunner(t *testing.T, timeout time.Duration, operations map[string][]string) *Runner {
	t.Helper()
	r := testRunner(t, timeout, operations)
	confinement := sandbox.MetadataConfinement()
	r.confinement = &confinement
	return r
}

func TestConfinedChildRunsLowIntegrityWithoutPrivileges(t *testing.T) {
	r := confinedRunner(t, 10*time.Second, map[string][]string{"token": confinedArgs("token")})
	result, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "token"})
	if err != nil {
		t.Fatal(err)
	}
	var report tokenReport
	if json.Unmarshal(result.Stdout, &report) != nil || report.Integrity != sandbox.LowIntegritySID || report.AdminAllow || report.Privileges > 1 {
		t.Fatalf("token %+v", report)
	}
	assertClean(t, r)
}

func TestConfinedChildCannotWriteOrSpawn(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	r := confinedRunner(t, 10*time.Second, map[string][]string{"write": confinedArgs("write", outside), "spawn": confinedArgs("spawn")})
	for _, operation := range []string{"write", "spawn"} {
		if _, err := r.Run(context.Background(), Request{Tool: "helper", Operation: operation}); err != nil {
			t.Fatalf("%s escaped confinement: %v", operation, err)
		}
	}
	if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("low-integrity child wrote a medium-integrity directory")
	}
	assertClean(t, r)
}

func TestConfinedChildReadsInheritedStdinAndArgvIsLiteral(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	values := []string{"", "a b", `quote"end`, `backslash\`, `x\"y`, "& echo escaped", "影片測試"}
	r := confinedRunner(t, 10*time.Second, map[string][]string{"stdin": confinedArgs("stdin"), "echo": helperArgs(append([]string{"echo"}, values...)...)})
	if _, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "stdin", Stdin: input}); err != nil {
		t.Fatal(err)
	}
	result, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "echo"})
	var actual []string
	if err != nil || json.Unmarshal(result.Stdout, &actual) != nil || strings.Join(actual, "\x00") != strings.Join(values, "\x00") {
		t.Fatalf("argv changed: %q %v", actual, err)
	}
	assertClean(t, r)
}

func TestConfinedChildLimitsAndTimeoutTerminateTheJob(t *testing.T) {
	r := confinedRunner(t, 500*time.Millisecond, map[string][]string{"allocate": confinedArgs("allocate"), "sleep": confinedArgs("sleep")})
	if _, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "allocate"}); err == nil {
		t.Fatal("job memory limit not applied")
	}
	started := time.Now()
	if _, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "sleep"}); err != ErrTimeout || time.Since(started) > 10*time.Second {
		t.Fatalf("timeout %v", err)
	}
	assertClean(t, r)
}

func TestConfinedStartRefusesIncompleteConfinement(t *testing.T) {
	r := testRunner(t, 5*time.Second, map[string][]string{"token": confinedArgs("token")})
	weak := sandbox.MetadataConfinement()
	weak.IntegritySID = "S-1-16-8192"
	r.confinement = &weak
	if _, err := r.Run(context.Background(), Request{Tool: "helper", Operation: "token"}); err != ErrStart || r.Stats().Started != 0 {
		t.Fatalf("weak confinement started: %v", err)
	}
	if sandbox.MetadataConfinement().UIRestrictions != windows.JOB_OBJECT_UILIMIT_DESKTOP|windows.JOB_OBJECT_UILIMIT_DISPLAYSETTINGS|windows.JOB_OBJECT_UILIMIT_EXITWINDOWS|windows.JOB_OBJECT_UILIMIT_GLOBALATOMS|
		windows.JOB_OBJECT_UILIMIT_HANDLES|windows.JOB_OBJECT_UILIMIT_READCLIPBOARD|windows.JOB_OBJECT_UILIMIT_SYSTEMPARAMETERS|windows.JOB_OBJECT_UILIMIT_WRITECLIPBOARD {
		t.Fatal("UI restriction bits differ from the Windows headers")
	}
	assertClean(t, r)
}
