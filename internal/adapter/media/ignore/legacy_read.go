package ignoresource

import "context"

type ruleReader interface{ OpenRule() (sourceFile, error) }
type legacyRuleReader struct {
	parent interface{ OpenLegacyRule() (sourceFile, error) }
}

func (r legacyRuleReader) OpenRule() (sourceFile, error) { return r.parent.OpenLegacyRule() }

// readLegacyRule reuses held-file identity, byte budgets, cancellation and
// complete content hashing. Unsupported readers fail rather than substitute
// the custom rule file or mistake unavailable access for a missing source.
func readLegacyRule(ctx context.Context, parent directory, remaining *int) (sourceStamp, []byte, error) {
	if ctx == nil || parent == nil || remaining == nil {
		return sourceStamp{}, nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return sourceStamp{}, nil, err
	}
	legacy, ok := parent.(interface{ OpenLegacyRule() (sourceFile, error) })
	if !ok {
		return sourceStamp{}, nil, ErrUnavailable
	}
	return readRule(ctx, legacyRuleReader{parent: legacy}, remaining)
}
