package sandbox

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSyscallPolicyRejectsArchitectureAndEscapeRoutes(t *testing.T) {
	const pid = 4242
	filter := syscallPolicy(pid)
	data := func(number uint32, args ...uint64) []byte {
		value := make([]byte, 64)
		binary.LittleEndian.PutUint32(value[0:], number)
		binary.LittleEndian.PutUint32(value[4:], unix.AUDIT_ARCH_X86_64)
		for index, arg := range args {
			binary.LittleEndian.PutUint64(value[16+index*8:], arg)
		}
		return value
	}
	allow, deny := uint32(unix.SECCOMP_RET_ALLOW), uint32(unix.SECCOMP_RET_ERRNO|unix.EPERM)
	for name, entry := range map[string]struct {
		data []byte
		want uint32
	}{
		"read":                    {data(unix.SYS_READ, 0), allow},
		"initial_exec":            {data(unix.SYS_EXECVEAT, executableFD, 0, 0, 0, unix.AT_EMPTY_PATH), allow},
		"alternate_exec_fd":       {data(unix.SYS_EXECVEAT, 0, 0, 0, 0, unix.AT_EMPTY_PATH), deny},
		"exec_fd_high_bits":       {data(unix.SYS_EXECVEAT, 1<<32|executableFD, 0, 0, 0, unix.AT_EMPTY_PATH), deny},
		"exec_path_flags":         {data(unix.SYS_EXECVEAT, executableFD, 0, 0, 0, 0), deny},
		"exec_flags_high_bits":    {data(unix.SYS_EXECVEAT, executableFD, 0, 0, 0, 1<<32|unix.AT_EMPTY_PATH), deny},
		"thread":                  {data(unix.SYS_CLONE, unix.CLONE_VM|unix.CLONE_SIGHAND|unix.CLONE_THREAD|unix.CLONE_FILES), allow},
		"process_clone":           {data(unix.SYS_CLONE, unix.CLONE_VM), deny},
		"namespace_clone":         {data(unix.SYS_CLONE, unix.CLONE_THREAD|unix.CLONE_NEWUSER), deny},
		"clone_high_bits":         {data(unix.SYS_CLONE, 1<<32|unix.CLONE_THREAD), deny},
		"clone3":                  {data(unix.SYS_CLONE3), unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		"own_signal":              {data(unix.SYS_TGKILL, pid, 4243, 23), allow},
		"other_signal":            {data(unix.SYS_TGKILL, 4200, 4201, 9), deny},
		"signal_high_bits":        {data(unix.SYS_KILL, 1<<32|pid, 9), deny},
		"query_limits":            {data(unix.SYS_PRLIMIT64, 0, unix.RLIMIT_AS, 0), allow},
		"change_limits":           {data(unix.SYS_PRLIMIT64, 0, unix.RLIMIT_AS, 1), deny},
		"change_limits_high_bits": {data(unix.SYS_PRLIMIT64, 0, unix.RLIMIT_AS, 1<<32), deny},
		"thread_name":             {data(unix.SYS_PRCTL, unix.PR_SET_NAME), allow},
		"disable_filter":          {data(unix.SYS_PRCTL, unix.PR_SET_SECCOMP), deny},
		"descriptor_flags":        {data(unix.SYS_FCNTL, 0, unix.F_GETFL), allow},
		"descriptor_duplicate":    {data(unix.SYS_FCNTL, 0, unix.F_DUPFD_CLOEXEC, 3), allow},
		"descriptor_cloexec":      {data(unix.SYS_FCNTL, 3, unix.F_SETFD, unix.FD_CLOEXEC), allow},
		"descriptor_bad_flags":    {data(unix.SYS_FCNTL, 3, unix.F_SETFD, 2), deny},
		"descriptor_flags_high":   {data(unix.SYS_FCNTL, 3, unix.F_SETFD, 1<<32|unix.FD_CLOEXEC), deny},
		"descriptor_command_high": {data(unix.SYS_FCNTL, 0, 1<<32|unix.F_GETFL), deny},
		"async_signal_owner":      {data(unix.SYS_FCNTL, 1, unix.F_SETOWN, 5555), deny},
		"async_signal_owner_ex":   {data(unix.SYS_FCNTL, 1, unix.F_SETOWN_EX, 1234), deny},
		"async_signal_number":     {data(unix.SYS_FCNTL, 1, unix.F_SETSIG, uint64(unix.SIGUSR1)), deny},
		"async_signal_enable":     {data(unix.SYS_FCNTL, 1, unix.F_SETFL, unix.O_WRONLY|unix.O_ASYNC), deny},
	} {
		t.Run(name, func(t *testing.T) {
			if got := evaluateBPF(t, filter, entry.data); got != entry.want {
				t.Fatalf("syscall policy got %#x want %#x", got, entry.want)
			}
		})
	}
	for _, number := range []uint32{unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_ACCEPT4, unix.SYS_SENDTO, unix.SYS_SENDMSG, unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_ENTER, unix.SYS_IO_URING_REGISTER, unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_PIDFD_GETFD, unix.SYS_FORK, unix.SYS_VFORK, unix.SYS_SETSID, unix.SYS_SETPGID, unix.SYS_EXECVE, unix.SYS_SETUID, unix.SYS_MOUNT, unix.SYS_UNSHARE, unix.SYS_MEMFD_CREATE, unix.SYS_BPF, 9999} {
		if evaluateBPF(t, filter, data(number)) != deny {
			t.Fatalf("escape syscall %d was allowed", number)
		}
	}
	wrongArch := data(unix.SYS_READ)
	binary.LittleEndian.PutUint32(wrongArch[4:], unix.AUDIT_ARCH_I386)
	for _, attack := range [][]byte{wrongArch, data(0x40000000 | unix.SYS_READ)} {
		if evaluateBPF(t, filter, attack) != unix.SECCOMP_RET_KILL_PROCESS {
			t.Fatal("alternate syscall architecture not terminated")
		}
	}
}

func evaluateBPF(t *testing.T, filter []unix.SockFilter, data []byte) uint32 {
	t.Helper()
	var accumulator uint32
	for instruction := 0; instruction < len(filter); instruction++ {
		current := filter[instruction]
		switch current.Code {
		case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
			if current.K > uint32(len(data)-4) {
				t.Fatal("BPF load outside seccomp_data")
			}
			accumulator = binary.LittleEndian.Uint32(data[current.K:])
		case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K:
			if accumulator == current.K {
				instruction += int(current.Jt)
			} else {
				instruction += int(current.Jf)
			}
		case unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K:
			if accumulator&current.K != 0 {
				instruction += int(current.Jt)
			} else {
				instruction += int(current.Jf)
			}
		case unix.BPF_RET | unix.BPF_K:
			return current.K
		default:
			t.Fatalf("unreviewed BPF instruction %#x", current.Code)
		}
	}
	t.Fatal("BPF policy fell through without verdict")
	return 0
}
