package scratch

import (
	"os"
	"strings"
	"testing"
)

func TestLinuxLiveness(t *testing.T) {
	own := ownerToken()
	if strings.HasSuffix(own, "-"+unknownStart) {
		t.Skip("process identity unavailable on this host")
	}
	start := own[strings.IndexByte(own, '-')+1:]
	boot, namespace, ticks, ok := splitLinuxStart(start)
	if !ok {
		t.Fatalf("own start identity does not parse")
	}
	other := func(b, n, tk string) string { return "b" + b + "n" + n + "t" + tk }
	otherBoot := strings.Repeat("0", 16)
	if boot == otherBoot {
		otherBoot = strings.Repeat("1", 16)
	}
	for _, c := range []struct {
		name  string
		owner owner
		want  liveness
	}{
		{"self", owner{os.Getpid(), start}, alive},
		{"pid reused", owner{os.Getpid(), other(boot, namespace, ticks+"1")}, dead},
		{"earlier boot", owner{os.Getpid(), other(otherBoot, namespace, ticks)}, dead},
		{"foreign namespace", owner{os.Getpid(), other(boot, namespace+"1", ticks)}, unknown},
		// Above the kernel's maximum pid_max, so it can never exist.
		{"exited", owner{1<<22 + 1, start}, dead},
		{"unreadable identity", owner{os.Getpid(), unknownStart}, unknown},
		{"malformed identity", owner{os.Getpid(), "c123"}, unknown},
	} {
		if got := processState(c.owner); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}
