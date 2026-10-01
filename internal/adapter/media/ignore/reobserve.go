package ignoresource

import (
	"context"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/platform/ignore"
)

// ReobserveDirectory reads an exact manifest directory independently of rule
// decisions. A newly excluding rule must not hide a retained descendant from
// verification. The first confirmed absent ancestor terminates the proof list.
func (r *Resolver) ReobserveDirectory(ctx context.Context, root, relative string) ([]DirectoryProof, error) {
	return r.reobserveDirectory(ctx, root, relative, openNativeRoot)
}

func (r *Resolver) reobserveDirectory(ctx context.Context, root, relative string, openRoot func(string) (directory, error)) (result []DirectoryProof, resultErr error) {
	if ctx == nil || r == nil || r.slots == nil || openRoot == nil {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRoot(root); err != nil {
		return nil, err
	}
	if relative != "." {
		if err := validateCandidate(relative); err != nil {
			return nil, err
		}
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	default:
		return nil, ErrBusy
	}
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
			result = nil
			resultErr = safeError(resultErr)
		}
	}()
	first, err := readDirectoryChain(ctx, root, relative, openRoot, &owned)
	if err != nil {
		return nil, err
	}
	second, err := readDirectoryChain(ctx, root, relative, openRoot, &owned)
	if err != nil {
		return nil, err
	}
	if len(first) != len(second) {
		return nil, ErrChanged
	}
	for i := range first {
		if first[i] != second[i] {
			return nil, ErrChanged
		}
	}
	return first, nil
}

func readDirectoryChain(ctx context.Context, root, relative string, openRoot func(string) (directory, error), owned *[]directory) ([]DirectoryProof, error) {
	current, err := openRoot(root)
	if err != nil {
		return nil, err
	}
	*owned = append(*owned, current)
	var components []string
	if relative != "." {
		components = strings.Split(relative, "/")
	}
	remaining := ignore.MaxTotalSourceBytes
	var result []DirectoryProof
	name := "."
	var parent [32]byte
	for i := 0; i <= len(components); i++ {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		sourcePath := ".jeleeignore"
		if name != "." {
			sourcePath = name + "/.jeleeignore"
		}
		if err = validateCandidate(sourcePath); err != nil {
			return nil, err
		}
		state, err := current.Stat()
		if err != nil {
			return nil, err
		}
		if state.kind != nodeDirectory {
			return nil, ErrUnsafe
		}
		stamp, _, err := readRule(ctx, current, &remaining)
		if err != nil {
			return nil, err
		}
		result = append(result, DirectoryProof{Directory: name, ParentIdentity: parent, Identity: state.identity, RulePresent: stamp.present, RuleIdentity: stamp.state.identity, RuleSize: stamp.state.size, RuleModifiedNano: stamp.state.modifiedUnixNano, RuleSHA256: stamp.digest})
		if i == len(components) {
			break
		}
		reader, ok := current.(scanDirectory)
		if !ok {
			return nil, ErrUnavailable
		}
		var absent bool
		current, absent, err = reader.OpenDirectoryOrAbsent(components[i])
		if err != nil {
			return nil, err
		}
		parent = state.identity
		if name == "." {
			name = components[i]
		} else {
			name += "/" + components[i]
		}
		if absent {
			result = append(result, DirectoryProof{Directory: name, ParentIdentity: parent, MissingDirectory: true})
			break
		}
		*owned = append(*owned, current)
	}
	return result, nil
}
