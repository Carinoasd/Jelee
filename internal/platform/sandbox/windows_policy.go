package sandbox

import (
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// WindowsMetadataPolicyVersion must change when the Windows confinement
// (token restrictions, integrity level, job limits or child policy) of the
// metadata and cover reads changes. See docs/adr/0011.
const WindowsMetadataPolicyVersion = "windows-metadata-sandbox-v1"

// Windows has no exec-in-place, so there is no helper process: the parent
// verifies and pins the tool image, then starts it directly with a
// restricted low-integrity token inside a locked-down Job Object. These are
// the platform-neutral parts of that policy; the system calls live in
// internal/platform/process (child_confined_windows.go).

// LowIntegritySID is the mandatory label of the confined token (Low IL).
// No-Write-Up then denies writes to every ordinary (medium-labelled) file,
// directory, registry key and named object; reads are not affected, so the
// tool still loads its own image and system DLLs.
const LowIntegritySID = "S-1-16-4096"

// denyOnlyGroupSIDs are the privileged groups turned into deny-only entries
// of the confined token. CreateRestrictedToken ignores entries that are not
// present in the service token, so the list can be fixed.
var denyOnlyGroupSIDs = []string{
	"S-1-5-32-544", // BUILTIN\Administrators
	"S-1-5-32-547", // BUILTIN\Power Users
	"S-1-5-32-548", // BUILTIN\Account Operators
	"S-1-5-32-549", // BUILTIN\Server Operators
	"S-1-5-32-550", // BUILTIN\Print Operators
	"S-1-5-32-551", // BUILTIN\Backup Operators
	"S-1-5-32-556", // BUILTIN\Network Configuration Operators
	"S-1-5-32-578", // BUILTIN\Hyper-V Administrators
	"S-1-5-114",    // NT AUTHORITY\Local account and member of Administrators group
}

// Job Object basic UI restriction bits (JOB_OBJECT_UILIMIT_*): desktop
// switching, display settings, ExitWindows, global atoms, USER handles of
// other processes, clipboard read/write and SystemParametersInfo.
const windowsUIRestrictions uint32 = 0x00000001 | 0x00000002 | 0x00000004 | 0x00000008 | 0x00000010 | 0x00000020 | 0x00000040 | 0x00000080

// Confinement describes how a direct (Windows) launcher's tool runs. Its
// only valid value is MetadataConfinement's; process applies it field by
// field and refuses anything Valid rejects.
type Confinement struct {
	// MemoryBytes is the per-process committed memory limit of the job.
	MemoryBytes uint64
	// UserCPUTime is the per-process user-mode CPU limit of the job. The
	// runner's wall-clock timeout still applies independently.
	UserCPUTime time.Duration
	// ActiveProcesses is the job's active process limit; 1 means the tool
	// itself and no child process.
	ActiveProcesses uint32
	// UIRestrictions is JOBOBJECT_BASIC_UI_RESTRICTIONS.UIRestrictionsClass.
	UIRestrictions uint32
	// IntegritySID is the mandatory label of the restricted token.
	IntegritySID string
	// DenyOnlyGroups are turned into deny-only group entries.
	DenyOnlyGroups []string
}

// Valid reports whether every restriction is present. A confinement with
// any missing field must never start a process.
func (c Confinement) Valid() bool {
	return c.MemoryBytes >= 64<<20 && c.MemoryBytes <= 4<<30 && c.UserCPUTime >= time.Second && c.UserCPUTime <= 10*time.Minute &&
		c.ActiveProcesses == 1 && c.UIRestrictions == windowsUIRestrictions && c.IntegritySID == LowIntegritySID &&
		len(c.DenyOnlyGroups) == len(denyOnlyGroupSIDs) && strings.Join(c.DenyOnlyGroups, ",") == strings.Join(denyOnlyGroupSIDs, ",")
}

// MetadataConfinement mirrors the Linux helper bounds: 2 GiB address space
// (here committed memory) and 30 s CPU. Every call returns fresh slices.
func MetadataConfinement() Confinement {
	return Confinement{
		MemoryBytes: 2 << 30, UserCPUTime: 30 * time.Second, ActiveProcesses: 1,
		UIRestrictions: windowsUIRestrictions, IntegritySID: LowIntegritySID,
		DenyOnlyGroups: append([]string(nil), denyOnlyGroupSIDs...),
	}
}

// validDirectPolicy accepts the Windows ffprobe identity: the vendor build
// is static, so a library closure is refused rather than silently ignored.
func validDirectPolicy(policy Policy) bool {
	return validDigest(policy.FFprobeSHA256) && len(policy.Libraries) == 0
}

// ValidWindowsToolPath accepts a clean absolute drive-letter path whose
// final element is name (case-insensitive). UNC, device (\\?\, \\.\),
// relative, forward-slash, alternate data stream and 8.3-ambiguous trailing
// dot/space forms are refused. It is pure string logic so it behaves the
// same on every build.
func ValidWindowsToolPath(value, name string) bool {
	if len(value) < 4 || len(value) > 259 || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	drive := value[0]
	if !(drive >= 'A' && drive <= 'Z' || drive >= 'a' && drive <= 'z') || value[1] != ':' || value[2] != '\\' {
		return false
	}
	elements := strings.Split(value[3:], `\`)
	for _, element := range elements {
		if element == "" || element == "." || element == ".." || strings.ContainsAny(element, `<>:"/|?*`) ||
			strings.HasSuffix(element, ".") || strings.HasSuffix(element, " ") {
			return false
		}
	}
	return strings.EqualFold(elements[len(elements)-1], name)
}

// WindowsToolLocation resolves a manifest project-relative path (slash
// separated, such as ".tools/media/...") against the installation root: the
// parent of the directory holding the running executable (bin\jelee.exe).
// It never consults the working directory or environment.
func WindowsToolLocation(executable, relative, name string) (string, bool) {
	if !ValidWindowsToolPath(executable, path.Base(strings.ReplaceAll(executable, `\`, "/"))) || !strings.HasPrefix(relative, ".tools/") ||
		strings.Contains(relative, `\`) || path.Clean(relative) != relative {
		return "", false
	}
	index := strings.LastIndexByte(executable, '\\')
	bin := executable[:index]
	parent := strings.LastIndexByte(bin, '\\')
	if parent < 2 {
		return "", false
	}
	location := bin[:parent] + `\` + strings.ReplaceAll(relative, "/", `\`)
	if !ValidWindowsToolPath(location, name) {
		return "", false
	}
	return location, true
}

// WindowsCommandLine joins argv into one CreateProcess command line using
// the CommandLineToArgvW / Microsoft C runtime quoting rules (the same rules
// as syscall.EscapeArg): backslashes are doubled only before a quote or the
// closing quote, quotes are escaped, and arguments with a space, tab or no
// characters are quoted. The program name is always quoted and must not
// contain a quote, because the first token has no escape syntax. NUL and an
// overlong result (CreateProcess limit, 32767 UTF-16 units) are refused.
func WindowsCommandLine(argv []string) (string, bool) {
	if len(argv) == 0 || argv[0] == "" || strings.ContainsAny(argv[0], "\"\x00") {
		return "", false
	}
	var line strings.Builder
	line.WriteByte('"')
	line.WriteString(argv[0])
	line.WriteByte('"')
	for _, argument := range argv[1:] {
		if strings.ContainsRune(argument, 0) || !utf8.ValidString(argument) {
			return "", false
		}
		line.WriteByte(' ')
		line.WriteString(escapeWindowsArgument(argument))
	}
	result := line.String()
	units := 0
	for _, r := range result {
		units++
		if r > 0xFFFF {
			units++
		}
	}
	if units >= 32767 {
		return "", false
	}
	return result, true
}

func escapeWindowsArgument(value string) string {
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t\"\\") {
		return value
	}
	quote := strings.ContainsAny(value, " \t")
	var out strings.Builder
	if quote {
		out.WriteByte('"')
	}
	slashes := 0
	for i := 0; i < len(value); i++ {
		switch c := value[i]; c {
		case '\\':
			slashes++
			out.WriteByte(c)
		case '"':
			// Double the pending backslashes, then escape the quote.
			out.WriteString(strings.Repeat(`\`, slashes+1))
			out.WriteByte(c)
			slashes = 0
		default:
			slashes = 0
			out.WriteByte(c)
		}
	}
	if quote {
		// Trailing backslashes would otherwise escape the closing quote.
		out.WriteString(strings.Repeat(`\`, slashes))
		out.WriteByte('"')
	}
	return out.String()
}
