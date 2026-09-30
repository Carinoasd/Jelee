// This synthetic executable is compiled only by sandbox integration tests.
// It is never installed or registered as a production tool.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/coverage"
	"strings"
	"time"
	"unsafe"

	"github.com/MoYuanCN/Jelee/internal/platform/sandbox"
	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--signal-control" {
		signals := make(chan os.Signal, 16)
		signal.Notify(signals, unix.SIGUSR1, unix.SIGIO)
		_, _ = os.Stdout.WriteString("ready\n")
		_, _ = io.Copy(io.Discard, os.Stdin)
		deadline := time.NewTimer(100 * time.Millisecond)
		count := 0
		for {
			select {
			case <-signals:
				count++
			case <-deadline.C:
				_ = json.NewEncoder(os.Stdout).Encode(count)
				return
			}
		}
	}
	if len(os.Args) > 1 && (os.Args[1] == "--test-helper" || os.Args[1] == "--test-helper-coverage") {
		collect := os.Args[1] == "--test-helper-coverage"
		var policy sandbox.Policy
		if json.Unmarshal([]byte(os.Getenv("JELEE_TEST_SANDBOX_POLICY")), &policy) != nil {
			os.Exit(60)
		}
		code := sandbox.RunHelper(os.Args[2:], policy)
		if collect {
			// Only this test wrapper exports coverage, through the inherited pipe.
			// It runs after a real failed exec; production has no callback or bypass.
			var meta, counters bytes.Buffer
			if err := coverage.WriteMeta(&meta); err != nil {
				fmt.Fprintln(os.Stderr, "test coverage metadata:", err)
				os.Exit(63)
			}
			if err := coverage.WriteCounters(&counters); err != nil {
				fmt.Fprintln(os.Stderr, "test coverage counters:", err)
				os.Exit(63)
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string][]byte{"meta": meta.Bytes(), "counters": counters.Bytes()})
		}
		os.Exit(code)
	}
	if len(os.Args) < 2 || os.Args[1] != "-hide_banner" {
		os.Exit(61)
	}
	var request struct {
		OutsideFile string
		WriteFile   string
		Directory   string
		SiblingPID  int
	}
	if json.NewDecoder(io.LimitReader(os.Stdin, 8192)).Decode(&request) != nil {
		os.Exit(62)
	}
	result := make(map[string]bool)
	_, err := os.ReadFile(request.OutsideFile)
	result["outside_read_denied"] = permission(err)
	_, err = os.ReadDir(request.Directory)
	result["directory_read_denied"] = permission(err)
	file, err := os.OpenFile(request.WriteFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	result["write_denied"] = permission(err)
	if err == nil {
		_ = file.Close()
	}
	_, err = os.ReadFile("/proc/self/environ")
	result["proc_content_denied"] = permission(err)
	for name, domain := range map[string]int{"ipv4": unix.AF_INET, "ipv6": unix.AF_INET6, "unix": unix.AF_UNIX, "netlink": unix.AF_NETLINK, "packet": unix.AF_PACKET} {
		fd, err := unix.Socket(domain, unix.SOCK_DGRAM, 0)
		result[name+"_socket_denied"] = errors.Is(err, unix.EPERM)
		if err == nil {
			_ = unix.Close(fd)
		}
	}
	_, err = unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	result["socketpair_denied"] = errors.Is(err, unix.EPERM)
	_, err = unix.Setsid()
	result["setsid_denied"] = errors.Is(err, unix.EPERM)
	err = unix.Setpgid(0, 0)
	result["setpgid_denied"] = errors.Is(err, unix.EPERM)
	_, _, errno := unix.RawSyscall(unix.SYS_IO_URING_SETUP, 0, 0, 0)
	result["io_uring_denied"] = errno == unix.EPERM
	_, _, errno = unix.RawSyscall(unix.SYS_PIDFD_OPEN, uintptr(os.Getppid()), 0, 0)
	result["pidfd_denied"] = errno == unix.EPERM
	_, _, errno = unix.RawSyscall(unix.SYS_CLONE3, 0, 0, 0)
	result["clone3_disabled"] = errno == unix.ENOSYS
	err = unix.Dup3(0, 255, 0)
	result["executable_fd_cannot_be_recreated"] = errors.Is(err, unix.EBADF)
	buffer := make([]byte, 256)
	n, _ := unix.Pread(3, buffer, 0)
	result["extra_fd_not_inherited"] = n <= 0 || !strings.Contains(string(buffer[:n]), "PRIVATE_EXTRA_FD")
	_, err = unix.Write(0, []byte("forbidden"))
	result["input_readonly"] = errors.Is(err, unix.EBADF)
	_, err = os.Stdin.Seek(0, io.SeekStart)
	result["input_seekable"] = err == nil
	var nofile, memory, size unix.Rlimit
	result["resource_bounds"] = unix.Getrlimit(unix.RLIMIT_NOFILE, &nofile) == nil && nofile.Cur == 128 && nofile.Max == 128 && unix.Getrlimit(unix.RLIMIT_AS, &memory) == nil && memory.Cur == 2<<30 && unix.Getrlimit(unix.RLIMIT_FSIZE, &size) == nil && size.Max == 0
	result["environment_cleared"] = os.Getenv("JELEE_TEST_SANDBOX_POLICY") == "" && os.Getenv("PRIVATE_TEST_SECRET") == "" && os.Getenv("LD_PRELOAD") == "" && len(os.Environ()) == 3
	_, err = unix.FcntlInt(1, unix.F_SETOWN, request.SiblingPID)
	result["fcntl_owner_denied"] = errors.Is(err, unix.EPERM)
	// Linux UAPI asm-generic/fcntl.h: F_OWNER_PID = 1.
	owner := struct{ Type, PID int32 }{Type: 1, PID: int32(request.SiblingPID)}
	_, _, errno = unix.RawSyscall(unix.SYS_FCNTL, 1, unix.F_SETOWN_EX, uintptr(unsafe.Pointer(&owner)))
	result["fcntl_owner_ex_denied"] = errno == unix.EPERM
	_, err = unix.FcntlInt(1, unix.F_SETSIG, int(unix.SIGUSR1))
	result["fcntl_signal_denied"] = errors.Is(err, unix.EPERM)
	_, err = unix.FcntlInt(1, unix.F_SETFL, unix.O_WRONLY|unix.O_ASYNC)
	result["fcntl_async_denied"] = errors.Is(err, unix.EPERM)
	_ = json.NewEncoder(os.Stdout).Encode(result)
}

func permission(err error) bool {
	return errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM)
}
