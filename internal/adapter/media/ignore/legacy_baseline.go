package ignoresource

import (
	"context"
	"crypto/sha256"
	"slices"
)

const LegacyBaselineProofVersion = "legacy-baseline-source-v1"

// Existing source evidence and a confirmed absent boundary are kept separate.
// No identities or source observations are fabricated below the missing child.
type LegacyBaselineObservation struct {
	Source           LegacyObservation `json:"-"`
	MissingDirectory DirectoryProof    `json:"-"`
	LookupDirectory  string            `json:"-"`
	token            [32]byte
}

func (o LegacyBaselineObservation) Token() [32]byte { return o.token }
func (LegacyBaselineObservation) String() string {
	return "legacy baseline observation (data redacted)"
}
func (LegacyBaselineObservation) GoString() string {
	return "legacy baseline observation (data redacted)"
}

func (r *Resolver) ObserveLegacyBaseline(ctx context.Context, root, relative string) (LegacyBaselineObservation, error) {
	return r.observeLegacyBaseline(ctx, root, relative, openNativeRoot)
}

func (r *Resolver) observeLegacyBaseline(ctx context.Context, root, relative string, openRoot func(string) (directory, error)) (result LegacyBaselineObservation, resultErr error) {
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
			result = LegacyBaselineObservation{}
			resultErr = safeError(resultErr)
		}
	}()
	var firstMissing, secondMissing DirectoryProof
	first, raw, err := readLegacyChainWithBoundary(ctx, root, relative, openRoot, &owned, &firstMissing)
	if err != nil {
		return result, err
	}
	second, _, err := readLegacyChainWithBoundary(ctx, root, relative, openRoot, &owned, &secondMissing)
	if err != nil {
		return result, err
	}
	if firstMissing != secondMissing || !slices.Equal(first, second) {
		return result, ErrChanged
	}
	result.LookupDirectory = relative
	result.MissingDirectory = firstMissing
	result.Source.chain = first
	result.Source.raw = raw
	h := sha256.New()
	hashString(h, LegacyBaselineProofVersion)
	hashString(h, root)
	hashString(h, relative)
	hashString(h, firstMissing.Directory)
	h.Write(firstMissing.ParentIdentity[:])
	for _, d := range first {
		hashString(h, d.path)
		h.Write(d.identity[:])
		if d.checked {
			hashUint(h, 1)
		} else {
			hashUint(h, 0)
		}
		if d.source.present {
			result.Source.Present = true
			result.Source.SourceDirectory = d.path
			hashUint(h, 1)
			hashStamp(h, d.source)
		} else {
			hashUint(h, 0)
		}
	}
	copy(result.token[:], h.Sum(nil))
	// The source is part of this baseline observation, not a standalone lookup.
	result.Source.token = result.token
	return result, nil
}
