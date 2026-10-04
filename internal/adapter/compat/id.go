package compat

import (
	"fmt"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ErrInvalidID reports an identifier that is not exactly one of the accepted
// wire forms, or that is the all-zero identifier.
var ErrInvalidID = fmt.Errorf("compat: invalid identifier: %w", domain.ErrInvalid)

// FormatID converts a native identifier (canonical lowercase dashed UUID) to
// the legacy wire form: 32 lowercase hex digits without dashes.
func FormatID(id string) (string, error) {
	if !domain.ValidID(id) || isNilID(id) {
		return "", ErrInvalidID
	}
	return strings.ReplaceAll(id, "-", ""), nil
}

// ParseID converts a legacy wire identifier to the native canonical form.
// Accepted inputs are exactly 32 hex digits, or the 36-character dashed form
// with dashes at positions 8, 13, 18 and 23; hex digits may be either case
// because clients echo identifiers they saw elsewhere. Everything else is
// rejected: braces, parentheses, surrounding or embedded white space, signs,
// other lengths, non-ASCII input and the all-zero identifier, which the legacy
// protocol uses to mean "absent" and never names a real entity.
func ParseID(wire string) (string, error) {
	var hex string
	switch len(wire) {
	case 32:
		hex = wire
	case 36:
		for _, i := range [...]int{8, 13, 18, 23} {
			if wire[i] != '-' {
				return "", ErrInvalidID
			}
		}
		hex = wire[:8] + wire[9:13] + wire[14:18] + wire[19:23] + wire[24:]
	default:
		return "", ErrInvalidID
	}
	var b strings.Builder
	b.Grow(36)
	for i := 0; i < len(hex); i++ {
		c := hex[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
			c += 'a' - 'A'
		default:
			return "", ErrInvalidID
		}
		if i == 8 || i == 12 || i == 16 || i == 20 {
			b.WriteByte('-')
		}
		b.WriteByte(c)
	}
	id := b.String()
	if isNilID(id) {
		return "", ErrInvalidID
	}
	return id, nil
}

func isNilID(id string) bool { return strings.Trim(id, "0-") == "" }
