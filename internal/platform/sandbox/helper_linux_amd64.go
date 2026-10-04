package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	executableFD   = 255
	supportedBuild = true
)

func executeHelper(profile Profile, policy Policy) error {
	// The caller is a dedicated helper process. Never unlock this thread after
	// applying policy; a caller receiving an error must immediately os.Exit.
	runtime.LockOSThread()
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		return ErrUnavailable
	}
	os.Clearenv()
	var input unix.Stat_t
	if unix.Fstat(0, &input) != nil || input.Mode&unix.S_IFMT != unix.S_IFREG {
		return ErrUnavailable
	}
	flags, err := unix.FcntlInt(0, unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return ErrUnavailable
	}
	if _, err := unix.Seek(0, 0, 0); err != nil {
		return ErrUnavailable
	}
	for _, fd := range []int{1, 2} {
		var output unix.Stat_t
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil || unix.Fstat(fd, &output) != nil || output.Mode&unix.S_IFMT != unix.S_IFIFO || flags&unix.O_ACCMODE != unix.O_WRONLY || flags&unix.O_ASYNC != 0 {
			return ErrUnavailable
		}
	}
	p, err := prepare(context.Background(), profile, policy)
	if err != nil {
		return err
	}
	defer p.close()
	if err := unix.Dup3(int(p.tool.file.Fd()), executableFD, unix.O_CLOEXEC); err != nil {
		return ErrUnavailable
	}
	defer unix.Close(executableFD)
	if unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != nil || unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0) != nil {
		return ErrUnavailable
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var capabilities [2]unix.CapUserData
	if unix.Capset(&header, &capabilities[0]) != nil {
		return ErrUnavailable
	}
	if err := restrictFilesystem(p); err != nil {
		return err
	}
	for resource, limit := range map[int]unix.Rlimit{
		unix.RLIMIT_NOFILE: {Cur: 128, Max: 128},
		unix.RLIMIT_NPROC:  {Cur: 128, Max: 128},
		unix.RLIMIT_AS:     {Cur: 2 << 30, Max: 2 << 30},
		unix.RLIMIT_CPU:    {Cur: 30, Max: 31},
		unix.RLIMIT_FSIZE:  {Cur: 0, Max: 0},
		unix.RLIMIT_CORE:   {Cur: 0, Max: 0},
	} {
		if unix.Setrlimit(resource, &limit) != nil {
			return ErrUnavailable
		}
	}
	// Unshare the locked thread's FD table so other runtime threads cannot add
	// descriptors after this sweep. Preserve internals until exec, then inherit
	// only stdio. FD255 closes on exec and cannot be recreated under NOFILE=128.
	if unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_UNSHARE|unix.CLOSE_RANGE_CLOEXEC) != nil {
		return ErrUnavailable
	}
	args := append([]string{"ffprobe"}, metadataArguments()...)
	environment := []string{"LANG=C", "LC_ALL=C", "TZ=UTC"}
	directories := make(map[string]bool)
	for _, library := range p.libraries {
		directories[filepath.Dir(library.path)] = true
	}
	var paths []string
	for directory := range directories {
		paths = append(paths, directory)
	}
	sort.Strings(paths)
	if len(paths) > 0 {
		environment = append(environment, "LD_LIBRARY_PATH="+strings.Join(paths, ":"))
	}
	argv, err := syscall.SlicePtrFromStrings(args)
	if err != nil {
		return ErrUnavailable
	}
	envp, err := syscall.SlicePtrFromStrings(environment)
	if err != nil {
		return ErrUnavailable
	}
	empty := []byte{0}
	filter := syscallPolicy(uint32(os.Getpid()))
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0) != nil { //nolint:gosec // G103: raw prctl(PR_SET_SECCOMP) has no safe wrapper
		return ErrUnavailable
	}
	// Raw execveat stays on the locked, restricted thread and executes the
	// verified file object. No pathname is reopened after digest verification.
	_, _, errno := unix.RawSyscall6(unix.SYS_EXECVEAT, executableFD, uintptr(unsafe.Pointer(&empty[0])), uintptr(unsafe.Pointer(&argv[0])), uintptr(unsafe.Pointer(&envp[0])), unix.AT_EMPTY_PATH, 0) //nolint:gosec // G103: raw execveat has no safe wrapper
	runtime.KeepAlive(argv)
	runtime.KeepAlive(envp)
	runtime.KeepAlive(empty)
	runtime.KeepAlive(filter)
	if errno != 0 {
		return ErrUnavailable
	}
	return ErrUnavailable
}

func restrictFilesystem(p *prepared) error {
	abi, _, errno := unix.RawSyscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 || abi < 3 {
		return ErrUnavailable
	}
	rights := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_SYM | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	if abi >= 5 {
		rights |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	// Only the v1/v3 filesystem field is needed; network is denied by the
	// architecture-checked seccomp allowlist, including non-TCP transports.
	ruleset, _, errno := unix.RawSyscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&rights)), unsafe.Sizeof(rights), 0) //nolint:gosec // G103: raw landlock_create_ruleset has no safe wrapper
	if errno != 0 {
		return ErrUnavailable
	}
	defer unix.Close(int(ruleset))
	add := func(file *os.File, access uint64) bool {
		rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(file.Fd())}
		_, _, errno := unix.RawSyscall6(unix.SYS_LANDLOCK_ADD_RULE, ruleset, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0) //nolint:gosec // G103: raw landlock_add_rule has no safe wrapper
		runtime.KeepAlive(file)
		return errno == 0
	}
	if !add(p.tool.file, unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_EXECUTE) {
		return ErrUnavailable
	}
	for index, library := range p.libraries {
		access := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)
		if index == p.interpreter {
			access |= unix.LANDLOCK_ACCESS_FS_EXECUTE
		}
		if !add(library.file, access) {
			return ErrUnavailable
		}
	}
	_, _, errno = unix.RawSyscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0)
	if errno != 0 {
		return ErrUnavailable
	}
	return nil
}
