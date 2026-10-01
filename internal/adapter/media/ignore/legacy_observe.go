package ignoresource

import (
	"context"
	"crypto/sha256"
	"strings"
)

// LegacyObservation describes the nearest fixed .ignore under the library
// root. It is not an execution lease or a persisted source-family proof.
type LegacyObservation struct {
	SourceDirectory string `json:"-"`
	Present         bool   `json:"-"`
	raw             []byte
	token           [32]byte
	chain           []legacyDirectoryObservation
}

func (o LegacyObservation) Bytes() []byte   { return append([]byte(nil), o.raw...) }
func (o LegacyObservation) Token() [32]byte { return o.token }
func (LegacyObservation) String() string    { return "legacy source observation (data redacted)" }
func (LegacyObservation) GoString() string  { return "legacy source observation (data redacted)" }

type legacyDirectoryObservation struct {
	path     string
	identity fileIdentity
	checked  bool
	source   sourceStamp
}

// ObserveLegacy starts at the supplied directory, not a candidate filename.
// File callers use its parent; directory callers use the directory itself.
// Only the nearest source is read; shadowed ancestors do not participate.
// Two complete native observations detect changes including a formerly absent
// nearer rule. Native open/stat retains the existing cancellation limitations.
func (r *Resolver) ObserveLegacy(ctx context.Context, root, relative string) (LegacyObservation, error) {
	return r.observeLegacy(ctx, root, relative, openNativeRoot)
}
func (r *Resolver) observeLegacy(ctx context.Context, root, relative string, openRoot func(string) (directory, error)) (result LegacyObservation, resultErr error) {
	if ctx == nil || r == nil || r.slots == nil || openRoot == nil {
		return result, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := validateRoot(root); err != nil {
		return result, err
	}
	if relative != "." {
		if err := validateCandidate(relative); err != nil {
			return result, err
		}
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return result, ErrBusy
	}
	defer func() { <-r.slots }()
	ctx, cancel := context.WithTimeout(ctx, MaxDuration)
	defer cancel()
	var owned []directory
	defer func() {
		for i := len(owned) - 1; i >= 0; i-- {
			if owned[i].Close() != nil {
				resultErr = ErrRead
			}
		}
		if ctx.Err() != nil {
			resultErr = ctx.Err()
		}
		if resultErr != nil {
			result = LegacyObservation{}
			resultErr = safeError(resultErr)
		}
	}()
	first, raw, err := readLegacyChain(ctx, root, relative, openRoot, &owned)
	if err != nil {
		return result, err
	}
	second, _, err := readLegacyChain(ctx, root, relative, openRoot, &owned)
	if err != nil {
		return result, err
	}
	if len(first) != len(second) {
		return result, ErrChanged
	}
	h := sha256.New()
	hashString(h, LegacySourceProofVersion)
	hashString(h, root)
	hashString(h, relative)
	for i, d := range first {
		if d != second[i] {
			return result, ErrChanged
		}
		hashString(h, d.path)
		h.Write(d.identity[:])
		if d.checked {
			hashUint(h, 1)
		} else {
			hashUint(h, 0)
		}
		if d.source.present {
			result.Present = true
			result.SourceDirectory = d.path
			hashUint(h, 1)
			hashStamp(h, d.source)
		} else {
			hashUint(h, 0)
		}
	}
	result.chain = first
	result.raw = raw
	copy(result.token[:], h.Sum(nil))
	return result, nil
}

func readLegacyChain(ctx context.Context, root, relative string, openRoot func(string) (directory, error), owned *[]directory) ([]legacyDirectoryObservation, []byte, error) {
	return readLegacyChainWithBoundary(ctx, root, relative, openRoot, owned, nil)
}

func readLegacyChainWithBoundary(ctx context.Context, root, relative string, openRoot func(string) (directory, error), owned *[]directory, missing *DirectoryProof) ([]legacyDirectoryObservation, []byte, error) {
	current, err := openRoot(root)
	if err != nil {
		return nil, nil, err
	}
	*owned = append(*owned, current)
	dirs := []directory{current}
	paths := []string{"."}
	missingPath := ""
	if relative != "." {
		path := ""
		for _, component := range strings.Split(relative, "/") {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			if path != "" {
				path += "/"
			}
			path += component
			var child directory
			if missing == nil {
				child, err = current.OpenDirectory(component)
			} else {
				reader, ok := current.(scanDirectory)
				if !ok {
					return nil, nil, ErrUnavailable
				}
				var absent bool
				child, absent, err = reader.OpenDirectoryOrAbsent(component)
				if err != nil {
					return nil, nil, err
				}
				if absent {
					missingPath = path
					break
				}
			}
			if err != nil {
				return nil, nil, err
			}
			current = child
			*owned = append(*owned, current)
			dirs = append(dirs, current)
			paths = append(paths, path)
		}
	}
	chain := make([]legacyDirectoryObservation, len(dirs))
	for i, d := range dirs {
		state, err := d.Stat()
		if err != nil {
			return nil, nil, err
		}
		if state.kind != nodeDirectory {
			return nil, nil, ErrUnsafe
		}
		chain[i] = legacyDirectoryObservation{path: paths[i], identity: state.identity}
	}
	if missingPath != "" {
		*missing = DirectoryProof{Directory: missingPath, ParentIdentity: chain[len(chain)-1].identity, MissingDirectory: true}
	}
	remaining := MaxCompileInputBytes
	for i := len(dirs) - 1; i >= 0; i-- {
		stamp, data, err := readLegacyRule(ctx, dirs[i], &remaining)
		if err != nil {
			return nil, nil, err
		}
		chain[i].checked = true
		chain[i].source = stamp
		if stamp.present {
			return chain, data, nil
		}
	}
	return chain, nil, nil
}
