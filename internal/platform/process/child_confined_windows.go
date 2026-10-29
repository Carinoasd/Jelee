package process

import (
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
)

// Windows media tool confinement (G09.6, ADR 0011): a restricted,
// low-integrity primary token and a Job Object with process, memory, CPU
// and UI limits. The child starts suspended, its job membership and token
// are verified, and only then does its first thread run.

// Not wrapped by golang.org/x/sys/windows v0.48.0: CreateRestrictedToken
// (advapi32) and IsProcessInJob (kernel32). LazySystemDLL loads only from
// the System32 directory.
var (
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procCreateRestrictedToken = advapi32.NewProc("CreateRestrictedToken")
	procIsProcessInJob        = kernel32.NewProc("IsProcessInJob")
)

const (
	// CreateRestrictedToken flag: remove every privilege except
	// SeChangeNotifyPrivilege.
	disableMaxPrivilege = 0x1
	// PROC_THREAD_ATTRIBUTE_CHILD_PROCESS_POLICY (Windows 10 1607+) with
	// PROCESS_CREATION_CHILD_PROCESS_RESTRICTED: the child cannot create
	// processes even outside the job's accounting.
	childProcessPolicyAttribute = 0x0002000e
	childProcessRestricted      = 0x1
	// PROC_THREAD_ATTRIBUTE_JOB_LIST, as in child_windows.go.
	jobListAttribute = 0x0002000d
	// SeChangeNotifyPrivilege is the only privilege DISABLE_MAX_PRIVILEGE keeps.
	changeNotifyPrivilege = "SeChangeNotifyPrivilege"
)

func startConfinedChild(path string, args []string, dir string, env []string, stdin, stdout, stderr *os.File, confinement sandbox.Confinement) (_ child, resultErr error) {
	if !confinement.Valid() {
		return nil, ErrInvalid
	}
	line, ok := sandbox.WindowsCommandLine(append([]string{path}, args...))
	if !ok {
		return nil, ErrInvalid
	}
	token, err := restrictedToken(confinement)
	if err != nil {
		return nil, err
	}
	defer func() { _ = token.Close() }()
	job, err := confinedJob(confinement)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			// KILL_ON_JOB_CLOSE terminates a suspended child that failed checks.
			_ = windows.CloseHandle(job)
		}
	}()
	attributes, err := windows.NewProcThreadAttributeList(3)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	if err := attributes.Update(jobListAttribute, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil { //nolint:gosec // G103: the Windows API takes a raw pointer to this fixed-layout value
		return nil, ErrUnsupported
	}
	policy := uint32(childProcessRestricted)
	if err := attributes.Update(childProcessPolicyAttribute, unsafe.Pointer(&policy), unsafe.Sizeof(policy)); err != nil { //nolint:gosec // G103: the Windows API takes a raw pointer to this fixed-layout value
		return nil, ErrUnsupported
	}
	handles, release, err := inheritableStdio(stdin, stdout, stderr)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil { //nolint:gosec // G103: the Windows API takes a raw pointer to this fixed-layout value
		return nil, err
	}
	startup := windows.StartupInfoEx{}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	startup.ShowWindow = windows.SW_HIDE
	startup.StdInput, startup.StdOutput, startup.StdErr = handles[0], handles[1], handles[2]
	startup.ProcThreadAttributeList = attributes.List()
	application, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	commandLine, err := windows.UTF16PtrFromString(line)
	if err != nil {
		return nil, err
	}
	directory, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil, err
	}
	block := environmentBlock(env)
	var info windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED)
	// A restricted version of the caller's own primary token needs no
	// SeAssignPrimaryTokenPrivilege.
	if err := windows.CreateProcessAsUser(token, application, commandLine, nil, nil, true, flags, &block[0], directory, &startup.StartupInfo, &info); err != nil {
		return nil, err
	}
	runtime.KeepAlive(handles)
	runtime.KeepAlive(block)
	runtime.KeepAlive(policy)
	started := &windowsChild{process: info.Process, job: job}
	// Verify before the first instruction: in this job, and running with the
	// low-integrity restricted token. Any doubt terminates the child.
	if err := verifyConfinedChild(info.Process, job, confinement); err != nil {
		_ = windows.TerminateProcess(info.Process, 1)
		_ = windows.CloseHandle(info.Thread)
		_ = windows.CloseHandle(info.Process)
		return nil, err
	}
	if _, err := windows.ResumeThread(info.Thread); err != nil {
		_ = windows.TerminateProcess(info.Process, 1)
		_ = windows.CloseHandle(info.Thread)
		_ = windows.CloseHandle(info.Process)
		return nil, err
	}
	_ = windows.CloseHandle(info.Thread)
	return started, nil
}

// restrictedToken derives the child's primary token from the service token:
// every privilege but SeChangeNotifyPrivilege removed, privileged groups
// deny-only, and the Low mandatory label. No restricting SIDs are added, so
// the user SID still grants read access to media the service user can read.
func restrictedToken(confinement sandbox.Confinement) (windows.Token, error) {
	var current windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT, &current); err != nil {
		return 0, err
	}
	defer func() { _ = current.Close() }()
	disabled := make([]windows.SIDAndAttributes, 0, len(confinement.DenyOnlyGroups))
	for _, value := range confinement.DenyOnlyGroups {
		sid, err := windows.StringToSid(value)
		if err != nil {
			return 0, ErrInvalid
		}
		disabled = append(disabled, windows.SIDAndAttributes{Sid: sid})
	}
	var restricted windows.Token
	// BOOL CreateRestrictedToken(ExistingTokenHandle, Flags, DisableSidCount,
	// SidsToDisable, DeletePrivilegeCount, PrivilegesToDelete,
	// RestrictedSidCount, SidsToRestrict, NewTokenHandle). SIDs that are not
	// in the token are ignored.
	ok, _, callErr := procCreateRestrictedToken.Call(uintptr(current), disableMaxPrivilege, uintptr(len(disabled)), uintptr(unsafe.Pointer(&disabled[0])), 0, 0, 0, 0, uintptr(unsafe.Pointer(&restricted))) //nolint:gosec // G103: the Windows API takes raw pointers to these fixed-layout values
	runtime.KeepAlive(disabled)
	if ok == 0 {
		if callErr == nil {
			callErr = ErrStart
		}
		return 0, callErr
	}
	label, err := windows.StringToSid(confinement.IntegritySID)
	if err != nil {
		_ = restricted.Close()
		return 0, ErrInvalid
	}
	mandatory := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: label, Attributes: windows.SE_GROUP_INTEGRITY}}
	if err := windows.SetTokenInformation(restricted, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&mandatory)), mandatory.Size()); err != nil { //nolint:gosec // G103: the Windows API takes a raw pointer to this fixed-layout value
		_ = restricted.Close()
		return 0, err
	}
	runtime.KeepAlive(label)
	if err := verifyToken(restricted, confinement); err != nil {
		_ = restricted.Close()
		return 0, err
	}
	return restricted, nil
}

// verifyToken reads the restrictions back instead of trusting the calls.
func verifyToken(token windows.Token, confinement sandbox.Confinement) error {
	integrity, err := tokenInformation(token, windows.TokenIntegrityLevel)
	if err != nil {
		return err
	}
	label := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&integrity[0])) //nolint:gosec // G103: the buffer holds a TOKEN_MANDATORY_LABEL written by the API
	if label.Label.Sid == nil || label.Label.Sid.String() != confinement.IntegritySID {
		return ErrStart
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return err
	}
	for _, group := range groups.AllGroups() {
		if group.Sid == nil {
			continue
		}
		for _, value := range confinement.DenyOnlyGroups {
			if group.Sid.String() == value && group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 {
				return ErrStart
			}
		}
	}
	buffer, err := tokenInformation(token, windows.TokenPrivileges)
	if err != nil {
		return err
	}
	var allowed windows.LUID
	name, err := windows.UTF16PtrFromString(changeNotifyPrivilege)
	if err != nil {
		return err
	}
	if err := windows.LookupPrivilegeValue(nil, name, &allowed); err != nil {
		return err
	}
	privileges := (*windows.Tokenprivileges)(unsafe.Pointer(&buffer[0])) //nolint:gosec // G103: the buffer holds a TOKEN_PRIVILEGES written by the API
	for _, privilege := range privileges.AllPrivileges() {
		if privilege.Luid != allowed {
			return ErrStart
		}
	}
	runtime.KeepAlive(integrity)
	runtime.KeepAlive(buffer)
	return nil
}

func tokenInformation(token windows.Token, class uint32) ([]byte, error) {
	var size uint32
	_ = windows.GetTokenInformation(token, class, nil, 0, &size)
	if size == 0 || size > 64<<10 {
		return nil, ErrStart
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, class, &buffer[0], size, &size); err != nil {
		return nil, err
	}
	return buffer, nil
}

// confinedJob creates the job before the child exists: kill on close, one
// active process, per-process committed memory and user CPU limits, crash
// dialogs suppressed, no breakaway, and every basic UI restriction.
func confinedJob(confinement sandbox.Confinement) (_ windows.Handle, resultErr error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		if resultErr != nil {
			_ = windows.CloseHandle(job)
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS |
		windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY | windows.JOB_OBJECT_LIMIT_PROCESS_TIME | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	limits.BasicLimitInformation.ActiveProcessLimit = confinement.ActiveProcesses
	// PerProcessUserTimeLimit is in 100-nanosecond ticks.
	limits.BasicLimitInformation.PerProcessUserTimeLimit = confinement.UserCPUTime.Nanoseconds() / 100
	limits.ProcessMemoryLimit = uintptr(confinement.MemoryBytes)
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil { //nolint:gosec // G103: the Windows API takes a raw pointer to this fixed-layout value
		return 0, err
	}
	ui := windows.JOBOBJECT_BASIC_UI_RESTRICTIONS{UIRestrictionsClass: confinement.UIRestrictions}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions, uintptr(unsafe.Pointer(&ui)), uint32(unsafe.Sizeof(ui))); err != nil { //nolint:gosec // G103: the Windows API takes a raw pointer to this fixed-layout value
		return 0, err
	}
	return job, nil
}

func verifyConfinedChild(process, job windows.Handle, confinement sandbox.Confinement) error {
	var member int32
	ok, _, callErr := procIsProcessInJob.Call(uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&member))) //nolint:gosec // G103: the Windows API takes a raw pointer to this BOOL
	if ok == 0 {
		if callErr == nil {
			callErr = ErrStart
		}
		return callErr
	}
	if member == 0 {
		return ErrStart
	}
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer func() { _ = token.Close() }()
	return verifyToken(token, confinement)
}
