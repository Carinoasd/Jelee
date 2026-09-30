package process

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsChild struct{ process, job windows.Handle }

func allowedProductionName(path string) bool {
	return strings.EqualFold(filepath.Base(path), "ffprobe.exe")
}

func executableAllowed(path string, _ os.FileInfo) bool {
	return strings.EqualFold(filepath.Ext(path), ".exe")
}
func platformEnvironment(env []string) []string {
	if root := os.Getenv("SystemRoot"); root != "" {
		env = append(env, "SystemRoot="+root)
	}
	return env
}
func validInput(file *os.File) bool {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	var status windows.IO_STATUS_BLOCK
	var access uint32
	// FileAccessInformation returns the existing handle's granted access.
	if windows.NtQueryInformationFile(windows.Handle(file.Fd()), &status, (*byte)(unsafe.Pointer(&access)), uint32(unsafe.Sizeof(access)), 8) != nil || access&windows.FILE_READ_DATA == 0 || access&(windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA|windows.FILE_WRITE_EA|windows.FILE_WRITE_ATTRIBUTES|windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER) != 0 {
		return false
	}
	_, err = file.Seek(0, io.SeekCurrent)
	return err == nil
}

func startChild(path string, args []string, dir string, env []string, stdin, stdout, stderr *os.File) (_ child, resultErr error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = windows.CloseHandle(job)
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, err
	}
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	// PROC_THREAD_ATTRIBUTE_JOB_LIST (Windows 10 / Server 2016+) atomically
	// assigns the job before the first instruction. No start/assign race.
	const jobList = 0x0002000d
	if err := attributes.Update(jobList, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		return nil, ErrUnsupported
	}
	var handles [3]windows.Handle
	current := windows.CurrentProcess()
	for i, file := range []*os.File{stdin, stdout, stderr} {
		if err := windows.DuplicateHandle(current, windows.Handle(file.Fd()), current, &handles[i], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return nil, err
		}
		defer windows.CloseHandle(handles[i])
	}
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
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
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{path}, args...)))
	if err != nil {
		return nil, err
	}
	directory, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return nil, err
	}
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	var info windows.ProcessInformation
	flags := uint32(windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(application, line, nil, nil, true, flags, &block[0], directory, &startup.StartupInfo, &info); err != nil {
		return nil, err
	}
	_ = windows.CloseHandle(info.Thread)
	runtime.KeepAlive(handles)
	runtime.KeepAlive(job)
	runtime.KeepAlive(block)
	return &windowsChild{process: info.Process, job: job}, nil
}

func (p *windowsChild) ready() error {
	_, err := windows.WaitForSingleObject(p.process, windows.INFINITE)
	return err
}
func (p *windowsChild) kill() error {
	if err := windows.TerminateJobObject(p.job, 1); err != nil {
		return err
	}
	// Termination is asynchronous. Observe the whole job, not only its root.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var accounting struct {
			TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime int64
			PageFaults, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
		}
		if err := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
			return err
		}
		if accounting.ActiveProcesses == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrCleanup
		}
		time.Sleep(time.Millisecond)
	}
}
func (p *windowsChild) reap() (int, error) {
	var code uint32
	err := windows.GetExitCodeProcess(p.process, &code)
	return int(code), err
}
func (p *windowsChild) close() error {
	a := windows.CloseHandle(p.process)
	b := windows.CloseHandle(p.job)
	if a != nil {
		return a
	}
	return b
}
