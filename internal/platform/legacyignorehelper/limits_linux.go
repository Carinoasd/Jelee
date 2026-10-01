package legacyignorehelper

import "golang.org/x/sys/unix"

func applyLimits() error {
	// This limits virtual address space, not resident memory or a cgroup.
	for resource, limit := range map[int]uint64{unix.RLIMIT_AS: MemoryBytes, unix.RLIMIT_CORE: 0} {
		current := unix.Rlimit{}
		if err := unix.Getrlimit(resource, &current); err != nil {
			return err
		}
		if current.Max < limit {
			limit = current.Max
		}
		target := unix.Rlimit{Cur: limit, Max: limit}
		if err := unix.Setrlimit(resource, &target); err != nil {
			return err
		}
	}
	return nil
}
