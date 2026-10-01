package sandbox

import "golang.org/x/sys/unix"

func load(offset uint32) unix.SockFilter {
	return unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: offset}
}
func compare(value uint32, yes, no uint8) unix.SockFilter {
	return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: yes, Jf: no, K: value}
}
func verdict(value uint32) unix.SockFilter {
	return unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: value}
}

func syscallPolicy(pid uint32) []unix.SockFilter {
	allow := verdict(unix.SECCOMP_RET_ALLOW)
	deny := verdict(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	filter := []unix.SockFilter{
		load(4), compare(unix.AUDIT_ARCH_X86_64, 1, 0), verdict(unix.SECCOMP_RET_KILL_PROCESS),
		load(0), {Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jf: 1}, verdict(unix.SECCOMP_RET_KILL_PROCESS),
	}
	block := func(number uint32, checks []unix.SockFilter) {
		filter = append(filter, compare(number, 0, uint8(len(checks))))
		filter = append(filter, checks...)
	}
	// The only executable FD cannot be allocated again after its first exec.
	block(unix.SYS_EXECVEAT, []unix.SockFilter{load(16), compare(executableFD, 1, 0), deny, load(20), compare(0, 1, 0), deny, load(48), compare(unix.AT_EMPTY_PATH, 1, 0), deny, load(52), compare(0, 1, 0), deny, allow})
	// Permit pthreads, not child processes or namespace creation. clone3 is
	// explicitly ENOSYS so libc can use its inspectable clone fallback.
	cloneMask := uint32(unix.CLONE_VM | unix.CLONE_FS | unix.CLONE_FILES | unix.CLONE_SIGHAND | unix.CLONE_THREAD | unix.CLONE_SYSVSEM | unix.CLONE_SETTLS | unix.CLONE_PARENT_SETTID | unix.CLONE_CHILD_CLEARTID | unix.CLONE_CHILD_SETTID | unix.CLONE_DETACHED)
	block(unix.SYS_CLONE, []unix.SockFilter{load(20), compare(0, 1, 0), deny, load(16), {Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: ^cloneMask, Jf: 1}, deny, {Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: unix.CLONE_THREAD, Jt: 1}, deny, allow})
	block(unix.SYS_CLONE3, []unix.SockFilter{verdict(unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS))})
	for _, number := range []uint32{unix.SYS_KILL, unix.SYS_TGKILL} {
		block(number, []unix.SockFilter{load(20), compare(0, 1, 0), deny, load(16), compare(pid, 1, 0), deny, allow})
	}
	// Only query rlimits; the target cannot raise or remove resource bounds.
	block(unix.SYS_PRLIMIT64, []unix.SockFilter{load(32), compare(0, 1, 0), deny, load(36), compare(0, 1, 0), deny, allow})
	block(unix.SYS_PRCTL, []unix.SockFilter{load(16), compare(unix.PR_SET_NAME, 2, 0), compare(unix.PR_GET_NAME, 1, 0), deny, allow})
	// Async ownership/notification commands can signal an unrelated same-UID
	// process without calling kill. Only descriptor duplication and flag queries
	// plus close-on-exec are needed; F_SETFL (including O_ASYNC) is denied.
	fcntl := []unix.SockFilter{load(28), compare(0, 1, 0), deny, load(24)}
	for _, command := range []uint32{unix.F_DUPFD, unix.F_DUPFD_CLOEXEC, unix.F_GETFD, unix.F_GETFL} {
		fcntl = append(fcntl, compare(command, 0, 1), allow)
	}
	fcntl = append(fcntl, compare(unix.F_SETFD, 1, 0), deny, load(36), compare(0, 1, 0), deny, load(32), compare(unix.FD_CLOEXEC, 1, 0), deny, allow)
	block(unix.SYS_FCNTL, fcntl)
	for _, number := range []uint32{
		unix.SYS_READ, unix.SYS_READV, unix.SYS_PREAD64, unix.SYS_WRITE, unix.SYS_WRITEV, unix.SYS_PWRITE64,
		unix.SYS_CLOSE, unix.SYS_LSEEK, unix.SYS_FSTAT, unix.SYS_NEWFSTATAT, unix.SYS_STAT, unix.SYS_LSTAT, unix.SYS_STATX,
		unix.SYS_OPEN, unix.SYS_OPENAT, unix.SYS_OPENAT2, unix.SYS_ACCESS, unix.SYS_FACCESSAT, unix.SYS_FACCESSAT2,
		unix.SYS_READLINK, unix.SYS_READLINKAT, unix.SYS_GETCWD, unix.SYS_GETDENTS64,
		unix.SYS_DUP, unix.SYS_DUP2, unix.SYS_DUP3,
		unix.SYS_MMAP, unix.SYS_MPROTECT, unix.SYS_MUNMAP, unix.SYS_MREMAP, unix.SYS_MADVISE, unix.SYS_BRK,
		unix.SYS_RT_SIGACTION, unix.SYS_RT_SIGPROCMASK, unix.SYS_RT_SIGRETURN, unix.SYS_RT_SIGPENDING, unix.SYS_RT_SIGTIMEDWAIT, unix.SYS_SIGALTSTACK,
		unix.SYS_FUTEX, unix.SYS_FUTEX_WAITV, unix.SYS_SET_TID_ADDRESS, unix.SYS_SET_ROBUST_LIST, unix.SYS_RSEQ, unix.SYS_ARCH_PRCTL,
		unix.SYS_GETPID, unix.SYS_GETTID, unix.SYS_GETPPID, unix.SYS_GETUID, unix.SYS_GETEUID, unix.SYS_GETGID, unix.SYS_GETEGID, unix.SYS_GETGROUPS,
		unix.SYS_GETRUSAGE, unix.SYS_GETRLIMIT, unix.SYS_UNAME, unix.SYS_SYSINFO,
		unix.SYS_CLOCK_GETTIME, unix.SYS_CLOCK_GETRES, unix.SYS_GETTIMEOFDAY, unix.SYS_NANOSLEEP, unix.SYS_CLOCK_NANOSLEEP,
		unix.SYS_SCHED_GETAFFINITY, unix.SYS_SCHED_YIELD, unix.SYS_GETRANDOM,
		unix.SYS_POLL, unix.SYS_PPOLL, unix.SYS_SELECT, unix.SYS_PSELECT6, unix.SYS_EPOLL_CREATE1, unix.SYS_EPOLL_CTL, unix.SYS_EPOLL_PWAIT, unix.SYS_EPOLL_WAIT, unix.SYS_EVENTFD2,
		unix.SYS_RESTART_SYSCALL, unix.SYS_EXIT, unix.SYS_EXIT_GROUP,
	} {
		filter = append(filter, compare(number, 0, 1), allow)
	}
	return append(filter, deny)
}
