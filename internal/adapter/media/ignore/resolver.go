package ignoresource

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

// Evaluate observes only the reachable ancestors of candidate. trustedRoot must
// come from trusted local configuration, never an untrusted request parameter.
// Reopening and hashing are observation checkpoints, not an atomic snapshot.
// Stalled native open/stat calls are not guaranteed to be hard-cancellable.
func (r *Resolver) Evaluate(ctx context.Context, trustedRoot, candidate string, kind ignore.Kind, options ignore.Options) (Observation, error) {
	return r.evaluate(ctx, trustedRoot, candidate, kind, options, sourceAccess{openRoot: openNativeRoot, compile: ignore.Compile})
}

func (r *Resolver) evaluate(ctx context.Context, root, candidate string, kind ignore.Kind, options ignore.Options, access sourceAccess) (result Observation, resultErr error) {
	if ctx == nil {
		return Observation{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	if r == nil || r.slots == nil || access.openRoot == nil || access.compile == nil ||
		(kind != ignore.File && kind != ignore.Directory) || options.Case > ignore.CaseASCIIInsensitive {
		return Observation{}, ErrInvalid
	}
	if err := validateRoot(root); err != nil {
		return Observation{}, err
	}
	if err := validateCandidate(candidate); err != nil {
		return Observation{}, err
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		return Observation{}, ErrBusy
	}
	ctx, cancel := context.WithTimeout(ctx, MaxDuration)
	defer cancel()
	var resources []io.Closer
	closeResources := func() error {
		var err error
		for i := len(resources) - 1; i >= 0; i-- {
			if resources[i].Close() != nil {
				err = ErrRead
			}
		}
		resources = nil
		return err
	}
	// On an unsuccessful path, close everything and prefer an observed context
	// cancellation. Successful paths close explicitly before cache publication.
	defer func() {
		if len(resources) != 0 {
			closeErr := closeResources()
			if err := ctx.Err(); err != nil {
				resultErr = err
			} else if closeErr != nil {
				resultErr = closeErr
			}
		}
		if resultErr != nil {
			if err := ctx.Err(); err != nil {
				resultErr = err
			}
			result = Observation{}
			resultErr = safeError(resultErr)
		}
	}()
	opened, err := access.openRoot(root)
	if err != nil {
		return Observation{}, err
	}
	resources = append(resources, opened)
	rootState, err := opened.Stat()
	if err != nil {
		return Observation{}, err
	}
	if rootState.kind != nodeDirectory {
		return Observation{}, ErrUnsafe
	}
	current := opened
	components := strings.Split(candidate, "/")
	var chain []directoryObservation
	var sources []ignore.Source
	var pending programCache
	var program *ignore.Program
	remaining := ignore.MaxTotalSourceBytes
	compileBytes, compileCalls, evaluateCalls := 0, 0, 0
	parentPath := ""
	var matched ignore.Match
	for i, component := range components {
		if err := ctx.Err(); err != nil {
			return Observation{}, err
		}
		sourcePath := ".jeleeignore"
		if parentPath != "" {
			sourcePath = parentPath + "/.jeleeignore"
		}
		if err := validateCandidate(sourcePath); err != nil {
			return Observation{}, err
		}
		state, err := current.Stat()
		if err != nil {
			return Observation{}, err
		}
		if state.kind != nodeDirectory {
			return Observation{}, ErrUnsafe
		}
		stamp, original, err := readRule(ctx, current, &remaining)
		if err != nil {
			return Observation{}, err
		}
		chain = append(chain, directoryObservation{parentPath, state.identity, stamp})
		if stamp.present {
			sources = append(sources, ignore.Source{Path: sourcePath, Text: original})
		}
		if program == nil || stamp.present {
			key := programKey(rootState.identity, chain, options.Case)
			var hit bool
			program, hit = pending.get(key)
			if !hit {
				program, hit = r.lookup(key)
			}
			if !hit {
				compileCalls++
				compileBytes += ignore.MaxTotalSourceBytes - remaining
				if compileCalls > ignore.MaxSources || compileBytes > MaxCompileInputBytes {
					return Observation{}, ErrWorkLimit
				}
				program, err = access.compile(ctx, sources, options)
				if err != nil {
					return Observation{}, err
				}
				if program == nil {
					return Observation{}, ErrRead
				}
				pending.put(key, program, cacheWeight(sources))
			}
		}
		prefix := component
		if parentPath != "" {
			prefix = parentPath + "/" + component
		}
		prefixKind := ignore.Directory
		if i == len(components)-1 {
			prefixKind = kind
		}
		evaluateCalls++
		if evaluateCalls > ignore.MaxPathComponents {
			return Observation{}, ErrWorkLimit
		}
		matched, err = program.Evaluate(ctx, prefix, prefixKind)
		if err != nil {
			return Observation{}, err
		}
		if i == len(components)-1 {
			break
		}
		if matched.Outcome == ignore.Exclude {
			// Return the same ancestor-blocked provenance as evaluating the full
			// candidate, while deliberately not opening the excluded subtree.
			evaluateCalls++
			if evaluateCalls > ignore.MaxPathComponents {
				return Observation{}, ErrWorkLimit
			}
			matched, err = program.Evaluate(ctx, candidate, kind)
			if err != nil {
				return Observation{}, err
			}
			break
		}
		current, err = current.OpenDirectory(component)
		if err != nil {
			return Observation{}, err
		}
		resources = append(resources, current)
		parentPath = prefix
	}
	// All first-pass directories stay open through the second pass. Each
	// observed absence is checked again and each present source is read/hash
	// checked again, even if every Program came from the warm shared cache.
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	current, err = access.openRoot(root)
	if err != nil {
		return Observation{}, err
	}
	resources = append(resources, current)
	remaining = ignore.MaxTotalSourceBytes
	for i, previous := range chain {
		if err := ctx.Err(); err != nil {
			return Observation{}, err
		}
		if i > 0 {
			current, err = current.OpenDirectory(components[i-1])
			if err != nil {
				return Observation{}, err
			}
			resources = append(resources, current)
		}
		state, err := current.Stat()
		if err != nil {
			return Observation{}, err
		}
		if state.kind != nodeDirectory || state.identity != previous.identity {
			return Observation{}, ErrChanged
		}
		stamp, _, err := readRule(ctx, current, &remaining)
		if err != nil {
			return Observation{}, err
		}
		if stamp != previous.source {
			return Observation{}, ErrChanged
		}
	}
	result = Observation{Match: matched, Diagnostics: program.Diagnostics(), token: observationToken(chain, options.Case), chain: chain}
	if err := closeResources(); err != nil {
		return Observation{}, err
	}
	// This lock and final check are the publication point. Cancellation after
	// publication cannot revoke the already completed immutable observation.
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	for e := pending.order.Front(); e != nil; e = e.Next() {
		v := e.Value.(cachedProgram)
		r.cache.put(v.key, v.program, v.weight)
	}
	return result, nil
}

func validateRoot(root string) error {
	if len(root) > MaxRootBytes {
		return ErrLimit
	}
	if !utf8.ValidString(root) || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsFunc(root, unicode.IsControl) {
		return ErrInvalid
	}
	return nil
}
func validateCandidate(candidate string) error {
	if len(candidate) > ignore.MaxPathBytes {
		return ErrLimit
	}
	if candidate == "" || !utf8.ValidString(candidate) || strings.ContainsAny(candidate, "\\:") || strings.ContainsFunc(candidate, unicode.IsControl) {
		return ErrInvalid
	}
	parts := strings.Split(candidate, "/")
	if len(parts) > ignore.MaxPathComponents {
		return ErrLimit
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ErrInvalid
		}
	}
	return nil
}
