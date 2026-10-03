package scratch

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type linuxIdentity struct {
	boot      string
	namespace string
}

// The boot id separates PID reuse across reboots; the PID namespace prevents
// judging a PID from another container that shares the temporary root.
var currentIdentity = sync.OnceValues(func() (linuxIdentity, bool) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return linuxIdentity{}, false
	}
	boot := strings.ReplaceAll(strings.TrimSpace(string(raw)), "-", "")
	if len(boot) < 16 || !lowerHex(boot[:16], 16) {
		return linuxIdentity{}, false
	}
	link, err := os.Readlink("/proc/self/ns/pid")
	if err != nil || !strings.HasPrefix(link, "pid:[") || !strings.HasSuffix(link, "]") {
		return linuxIdentity{}, false
	}
	namespace := link[len("pid:[") : len(link)-1]
	if !allDigits(namespace) || len(namespace) > 20 {
		return linuxIdentity{}, false
	}
	return linuxIdentity{boot: boot[:16], namespace: namespace}, true
})

func platformStart() (string, bool) {
	identity, ok := currentIdentity()
	if !ok {
		return "", false
	}
	ticks, err := startTicks("self")
	if err != nil {
		return "", false
	}
	return "b" + identity.boot + "n" + identity.namespace + "t" + ticks, true
}

// startTicks reads field 22 of /proc/<pid>/stat. The command name may contain
// spaces or parentheses, so fields are counted after the last ')'.
func startTicks(pid string) (string, error) {
	raw, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return "", err
	}
	text := string(raw)
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return "", errors.New("stat")
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) < 20 || !allDigits(fields[19]) || len(fields[19]) > 20 {
		return "", errors.New("stat")
	}
	return fields[19], nil
}

func splitLinuxStart(start string) (boot, namespace, ticks string, ok bool) {
	if len(start) < 1+16+2+2 || start[0] != 'b' || !lowerHex(start[1:17], 16) || start[17] != 'n' {
		return "", "", "", false
	}
	rest := start[18:]
	t := strings.IndexByte(rest, 't')
	if t < 1 || !allDigits(rest[:t]) || !allDigits(rest[t+1:]) {
		return "", "", "", false
	}
	return start[1:17], rest[:t], rest[t+1:], true
}

func platformState(o owner) liveness {
	identity, ok := currentIdentity()
	if !ok {
		return unknown
	}
	boot, namespace, ticks, ok := splitLinuxStart(o.start)
	if !ok {
		return unknown
	}
	if boot != identity.boot {
		// Every process of an earlier boot has exited.
		return dead
	}
	if namespace != identity.namespace {
		return unknown
	}
	current, err := startTicks(strconv.Itoa(o.pid))
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH):
		return dead
	case err != nil:
		return unknown
	case current != ticks:
		// The PID now belongs to a different process.
		return dead
	default:
		return alive
	}
}

// Only objects of the effective user are candidates. Same-user processes are
// also visible under hidepid, so a missing /proc entry really means exited.
func ownedByCurrentUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Geteuid())
}
