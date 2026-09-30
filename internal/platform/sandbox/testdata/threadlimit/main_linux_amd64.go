// This syscall-only thread fixture is compiled with the pinned Go toolchain.
// It is never installed or registered as a production executable.
package main

import (
	"encoding/json"
	"os"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The child stays in assembly, performs only futex/exit syscalls and never
// enters Go code or uses the parent's TLS. Its signals remain blocked.
func cloneThread(stackTop uintptr, stop, tid *uint32) int64

func main() {
	runtime.GOMAXPROCS(1)
	runtime.LockOSThread()
	var limit unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NPROC, &limit) != nil || limit.Cur != 128 || limit.Max != 128 {
		os.Exit(70)
	}
	stacks := make([][]byte, 256)
	for index := range stacks {
		stack, err := unix.Mmap(-1, 0, 64<<10, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
		if err != nil {
			os.Exit(71)
		}
		stacks[index] = stack
	}
	var stop, tids [256]uint32
	// clone inherits this per-thread mask. Keep all raw children away from Go
	// signal handlers; SIGKILL/SIGSTOP retain their normal kernel behavior.
	mask, previous := ^uint64(0), uint64(0)
	_, _, errno := unix.RawSyscall6(unix.SYS_RT_SIGPROCMASK, unix.SIG_SETMASK, uintptr(unsafe.Pointer(&mask)), uintptr(unsafe.Pointer(&previous)), 8, 0, 0)
	if errno != 0 {
		os.Exit(72)
	}
	created := 0
	var result int64
	for created < len(stacks) {
		stack := stacks[created]
		result = cloneThread(uintptr(unsafe.Pointer(&stack[len(stack)-16])), &stop[0], &tids[created])
		if result < 0 {
			break
		}
		created++
	}
	_, _, errno = unix.RawSyscall6(unix.SYS_RT_SIGPROCMASK, unix.SIG_SETMASK, uintptr(unsafe.Pointer(&previous)), 0, 8, 0, 0)
	if errno != 0 {
		os.Exit(73)
	}
	atomic.StoreUint32(&stop[0], 1)
	_, _, _ = unix.RawSyscall6(unix.SYS_FUTEX, uintptr(unsafe.Pointer(&stop[0])), 128|1, 1<<31-1, 0, 0, 0)
	deadline := time.Now().Add(3 * time.Second)
	for index := 0; index < created; index++ {
		for atomic.LoadUint32(&tids[index]) != 0 {
			if time.Now().After(deadline) {
				os.Exit(74)
			}
			runtime.Gosched()
		}
	}
	for _, stack := range stacks {
		_ = unix.Munmap(stack)
	}
	if result != -int64(unix.EAGAIN) || created < 1 || created >= 128 {
		os.Exit(75)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"threadsCreated": created, "limitReached": true})
	runtime.KeepAlive(stop)
	runtime.KeepAlive(tids)
}
