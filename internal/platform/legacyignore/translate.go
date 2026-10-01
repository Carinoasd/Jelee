// Package legacyignore contains the versioned legacy .ignore translation.
// It does not read sources or enable legacy scanning.
// Translation order derives from Ignore 0.2.1, Copyright (c) 2020 Hardik Goel.
// See LICENSE.ignore and docs/ignore-legacy-audit.md.
package legacyignore

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

const TranslationVersion = "ignore-0.2.1-f7c6f07"
const MaxPatternBytes = 4096

var ErrInvalid = errors.New("legacy_ignore_invalid_pattern")

// Expression is the upstream regex text, not a compiled or authorized matcher.
// In particular, callers must not silently treat unsupported .NET regex syntax
// as an invalid upstream rule or as an unmatched path.
type Expression struct {
	Pattern  string
	Negative bool
	Inactive bool
}

var (
	trailingSpaces  = regexp.MustCompile(`\\?[\t\n\v\f\r \x{0085}\p{Z}]+$`)
	escapedSpaces   = regexp.MustCompile(`\\[\t\n\v\f\r \x{0085}\p{Z}]`)
	middleSlash     = regexp.MustCompile(`^([^/\^]+/[^/]+)`)
	trailingSlash   = regexp.MustCompile(`^([^/]+)/$`)
	noTrailingSlash = regexp.MustCompile(`([^/$]+)$`)
)

// Translate applies only the fixed dependency's ordered string transforms.
// Wrapper-level trimming, nearest-file lookup, empty-file policy, case/culture
// behavior and regex execution are separate contracts.
func Translate(pattern string) (Expression, error) {
	if !utf8.ValidString(pattern) || len(pattern) > MaxPatternBytes {
		return Expression{}, ErrInvalid
	}
	if strings.TrimSpace(pattern) == "" || strings.HasPrefix(pattern, "#") {
		return Expression{Inactive: true}, nil
	}
	negative := false
	if strings.HasPrefix(pattern, `\!`) || strings.HasPrefix(pattern, `\#`) {
		pattern = pattern[1:]
	} else if strings.HasPrefix(pattern, "!") {
		negative, pattern = true, pattern[1:]
	}
	pattern = trailingSpaces.ReplaceAllStringFunc(pattern, func(s string) string {
		if strings.HasPrefix(s, `\`) {
			return " "
		}
		return ""
	})
	pattern = escapedSpaces.ReplaceAllString(pattern, " ")
	pattern = strings.ReplaceAll(pattern, ".", `\.`)
	pattern = strings.ReplaceAll(pattern, "+", `\+`)
	// The upstream negative lookahead is evaluated at '?' itself, so even a
	// preceding backslash does not prevent this replacement.
	pattern = strings.ReplaceAll(pattern, "?", "[^/]")
	if pattern != "" && !strings.Contains(pattern, "/") {
		pattern = "(^|/)" + pattern
	}
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '*' && i > 0 && pattern[i-1] != '*' && (i+1 == len(pattern) || pattern[i+1] != '*') {
			b.WriteString("[^/]*")
		} else {
			b.WriteByte(pattern[i])
		}
	}
	pattern = b.String()
	if strings.HasPrefix(pattern, "*") && !strings.HasPrefix(pattern, "**") {
		pattern = ".*" + pattern[1:]
	}
	if strings.HasPrefix(pattern, "**/") {
		pattern = ".*" + pattern[3:]
	} else if strings.HasPrefix(pattern, "**") {
		pattern = ".*" + pattern[2:]
	}
	b.Reset()
	for i := 0; i < len(pattern); {
		if i > 0 && pattern[i-1] == '/' && strings.HasPrefix(pattern[i:], "**/") {
			b.WriteString("(.*/)?")
			i += 3
		} else {
			b.WriteByte(pattern[i])
			i++
		}
	}
	pattern = b.String()
	if strings.HasPrefix(pattern, "/") {
		pattern = "^" + pattern[1:]
	}
	if strings.HasSuffix(pattern, "**") {
		pattern = pattern[:len(pattern)-2] + ".*$"
	}
	pattern = strings.ReplaceAll(pattern, "**", "[^/]*")
	pattern = middleSlash.ReplaceAllString(pattern, "^${1}")
	pattern = trailingSlash.ReplaceAllString(pattern, "(/|^)${1}/")
	pattern = noTrailingSlash.ReplaceAllString(pattern, "${1}(/.*)?$")
	pattern = noTrailingSlash.ReplaceAllString(pattern, "${1}$")
	return Expression{Pattern: pattern, Negative: negative}, nil
}
