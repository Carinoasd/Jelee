// Package scratch names private temporary objects after the process that
// owns them and removes crash leftovers whose owner has provably exited.
//
// A name embeds the owner's PID and a platform start identity, so liveness is
// decided from the name alone, without lock files or a creation window:
//
//	<prefix>o<pid>-<start>-<32 hex><suffix>
//
// Linux start identities are b<boot id>n<pid namespace>t<start ticks>;
// Windows ones are c<process creation FILETIME>. An owner is dead only when
// the PID no longer exists, or exists with a different start identity, or the
// identity belongs to an earlier boot. Anything else (a foreign PID namespace,
// an unreadable process table, an unsupported platform) keeps the object.
package scratch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors carry fixed codes only; they never wrap OS errors containing paths.
var (
	ErrRoot    = errors.New("scratch_root_unavailable")
	ErrCreate  = errors.New("scratch_create_failed")
	ErrInvalid = errors.New("scratch_invalid")
)

// Kind is one fixed naming family. Kinds are package-defined so a sweep can
// never be pointed at arbitrary name patterns.
type Kind struct {
	prefix, suffix string
	dir            bool
}

var (
	// Service-lifetime runner roots below os.TempDir().
	ServiceProbe  = Kind{prefix: "jelee-service-probe-", dir: true}
	ServiceIgnore = Kind{prefix: "jelee-service-ignore-", dir: true}
	// Startup/CLI health-check root below os.TempDir().
	ProbeCheck = Kind{prefix: "jelee-probe-check-", dir: true}
	// Staged local image copies below the configured image temp root.
	ImageStage = Kind{prefix: "image-", suffix: ".partial"}
)

// unknownStart marks an owner whose start identity could not be read. Such
// objects are never removed by a sweep.
const unknownStart = "u"

var ownerToken = sync.OnceValue(func() string {
	start, ok := platformStart()
	if !ok || !validStart(start) {
		start = unknownStart
	}
	return "o" + strconv.Itoa(os.Getpid()) + "-" + start
})

// NewName returns a fresh name of this kind owned by the current process.
func (k Kind) NewName() (string, error) {
	if k.prefix == "" {
		return "", ErrInvalid
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", ErrCreate
	}
	return k.prefix + ownerToken() + "-" + hex.EncodeToString(nonce[:]) + k.suffix, nil
}

// MkdirOwned creates a private 0700 directory of a directory kind below
// parent, or below os.TempDir() when parent is empty. It returns the path.
func MkdirOwned(parent string, k Kind) (string, error) {
	if !k.dir {
		return "", ErrInvalid
	}
	if parent == "" {
		parent = os.TempDir()
	}
	for range 8 {
		name, err := k.NewName()
		if err != nil {
			return "", err
		}
		path := filepath.Join(parent, name)
		err = os.Mkdir(path, 0700)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", ErrCreate
		}
	}
	return "", ErrCreate
}

type owner struct {
	pid   int
	start string
}

// parse accepts only exact names produced by NewName for one of kinds.
func parse(name string, kinds []Kind) (Kind, owner, bool) {
	for _, k := range kinds {
		if !strings.HasPrefix(name, k.prefix) || !strings.HasSuffix(name, k.suffix) || len(name) < len(k.prefix)+len(k.suffix) {
			continue
		}
		parts := strings.Split(name[len(k.prefix):len(name)-len(k.suffix)], "-")
		if len(parts) != 3 || len(parts[0]) < 2 || parts[0][0] != 'o' || !validStart(parts[1]) || !lowerHex(parts[2], 32) {
			continue
		}
		digits := parts[0][1:]
		if len(digits) > 10 || digits[0] == '0' || !allDigits(digits) {
			continue
		}
		pid, err := strconv.ParseUint(digits, 10, 32)
		if err != nil || pid == 0 {
			continue
		}
		return k, owner{pid: int(pid), start: parts[1]}, true
	}
	return Kind{}, owner{}, false
}

func validStart(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func allDigits(value string) bool {
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return value != ""
}

type liveness int

const (
	unknown liveness = iota
	alive
	dead
)

// processState is replaced only by package tests.
var processState = func(o owner) liveness {
	if o.start == unknownStart {
		return unknown
	}
	return platformState(o)
}

// Limits bound one sweep. Zero fields use DefaultLimits values.
type Limits struct {
	// Entries bounds top-level directory entries examined in the root.
	Entries int
	// Removals bounds leftover objects (files or trees) removed.
	Removals int
	// TreeEntries bounds nested entries removed across all trees.
	TreeEntries int
	// Depth bounds nesting below a leftover directory.
	Depth   int
	Timeout time.Duration
}

func DefaultLimits() Limits {
	return Limits{Entries: 4096, Removals: 64, TreeEntries: 65536, Depth: 16, Timeout: 5 * time.Second}
}

// Report holds counts only; it never names paths or entries.
type Report struct {
	Examined int
	Removed  int
	// Alive and Unknown count owned names that were kept.
	Alive   int
	Unknown int
	// Foreign counts entries that are not exact owned names of the expected
	// type and owner account. They are never touched.
	Foreign int
	Failed  int
	// Incomplete is set when a limit or the deadline stopped the sweep.
	Incomplete bool
}

var errLimit = errors.New("scratch_limit")

// Sweep removes leftovers of kinds directly below root whose owner process
// has provably exited. Root must be an absolute, non-symlink directory. All
// operations go through os.Root handles, so symlinks are removed as links and
// are never followed out of the root or out of a leftover tree.
func Sweep(ctx context.Context, root string, kinds []Kind, limits Limits) (report Report, resultErr error) {
	if ctx == nil || len(kinds) == 0 || !filepath.IsAbs(root) {
		return report, ErrInvalid
	}
	defaults := DefaultLimits()
	if limits.Entries <= 0 {
		limits.Entries = defaults.Entries
	}
	if limits.Removals <= 0 {
		limits.Removals = defaults.Removals
	}
	if limits.TreeEntries <= 0 {
		limits.TreeEntries = defaults.TreeEntries
	}
	if limits.Depth <= 0 {
		limits.Depth = defaults.Depth
	}
	if limits.Timeout <= 0 {
		limits.Timeout = defaults.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	root = filepath.Clean(root)
	before, err := os.Lstat(root)
	if err != nil || !before.IsDir() || before.Mode()&fs.ModeSymlink != 0 {
		return report, ErrRoot
	}
	held, err := os.OpenRoot(root)
	if err != nil {
		return report, ErrRoot
	}
	defer held.Close()
	if after, err := held.Stat("."); err != nil || !os.SameFile(before, after) {
		return report, ErrRoot
	}
	type candidate struct {
		name string
		info fs.FileInfo
		dir  bool
	}
	var doomed []candidate
	listing, err := held.Open(".")
	if err != nil {
		return report, ErrRoot
	}
	for !report.Incomplete {
		if ctx.Err() != nil {
			report.Incomplete = true
			break
		}
		entries, readErr := listing.ReadDir(256)
		for _, entry := range entries {
			if report.Examined >= limits.Entries {
				report.Incomplete = true
				break
			}
			report.Examined++
			kind, who, ok := parse(entry.Name(), kinds)
			wantType := fs.FileMode(0)
			if kind.dir {
				wantType = fs.ModeDir
			}
			if !ok || entry.Type() != wantType {
				report.Foreign++
				continue
			}
			info, err := held.Lstat(entry.Name())
			if err != nil || info.Mode().Type() != wantType || !ownedByCurrentUser(info) {
				report.Foreign++
				continue
			}
			switch processState(who) {
			case alive:
				report.Alive++
			case dead:
				if len(doomed) >= limits.Removals {
					report.Incomplete = true
					continue
				}
				doomed = append(doomed, candidate{name: entry.Name(), info: info, dir: kind.dir})
			default:
				report.Unknown++
			}
		}
		if errors.Is(readErr, io.EOF) || readErr == nil && len(entries) == 0 {
			break
		}
		if readErr != nil {
			report.Incomplete = true
			resultErr = ErrRoot
			break
		}
	}
	if listing.Close() != nil && resultErr == nil {
		resultErr = ErrRoot
	}
	budget := limits.TreeEntries
	for _, c := range doomed {
		if ctx.Err() != nil {
			report.Incomplete = true
			break
		}
		err := removeOwned(ctx, held, c.name, c.info, c.dir, limits.Depth, &budget)
		switch {
		case err == nil:
			report.Removed++
		case errors.Is(err, errLimit) || ctx.Err() != nil:
			report.Incomplete = true
		default:
			report.Failed++
		}
		if budget <= 0 {
			report.Incomplete = true
			break
		}
	}
	return report, resultErr
}

func removeOwned(ctx context.Context, parent *os.Root, name string, listed fs.FileInfo, dir bool, depth int, budget *int) error {
	if !dir {
		// Root.Remove never follows a final-component symlink; the identity
		// check rejects a swap since listing.
		current, err := parent.Lstat(name)
		if err != nil || !os.SameFile(listed, current) || !current.Mode().IsRegular() {
			return ErrInvalid
		}
		return removeEntry(parent, name)
	}
	tree, err := parent.OpenRoot(name)
	if err != nil {
		return ErrInvalid
	}
	opened, err := tree.Stat(".")
	if err != nil || !os.SameFile(listed, opened) {
		_ = tree.Close()
		return ErrInvalid
	}
	err = removeTree(ctx, tree, ".", 0, depth, budget)
	if closeErr := tree.Close(); err == nil && closeErr != nil {
		err = ErrInvalid
	}
	if err != nil {
		return err
	}
	return removeEntry(parent, name)
}

// removeTree empties dir inside tree. Directory entries report lstat types,
// so symlinks are removed as links and only real directories are descended.
// Paths are resolved by the leftover's own os.Root, confining any race.
func removeTree(ctx context.Context, tree *os.Root, dir string, depth, maxDepth int, budget *int) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f, err := tree.Open(dir)
		if err != nil {
			return ErrInvalid
		}
		entries, readErr := f.ReadDir(256)
		if f.Close() != nil {
			return ErrInvalid
		}
		if len(entries) == 0 {
			if readErr == nil || errors.Is(readErr, io.EOF) {
				return nil
			}
			return ErrInvalid
		}
		for _, entry := range entries {
			if *budget <= 0 {
				return errLimit
			}
			child := filepath.Join(dir, entry.Name())
			if entry.Type() == fs.ModeDir {
				if depth+1 > maxDepth {
					return errLimit
				}
				if err := removeTree(ctx, tree, child, depth+1, maxDepth, budget); err != nil {
					return err
				}
			}
			if err := removeEntry(tree, child); err != nil {
				return err
			}
			*budget--
		}
	}
}

func removeEntry(r *os.Root, name string) error {
	if err := r.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ErrInvalid
	}
	return nil
}
