package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Content access rules (G48.1, G48.4) narrow what the library grants show a
// user. docs/access-control.md describes the precedence; storage enforces
// it in the unified filter.
const (
	// ParentalRatingLevelMax is the highest rating level, an age in years.
	ParentalRatingLevelMax = 21
	// ContentBlockedTagsMax bounds the blocked tags and genres of a user.
	ContentBlockedTagsMax = 100
	// ContentBlockedTagMax bounds one blocked tag in UTF-8 bytes.
	ContentBlockedTagMax = 128
	// ItemAccessRulesMax bounds the explicit item rules of a user.
	ItemAccessRulesMax = 1000
)

// ItemAccessEffect is the outcome of an explicit item rule.
type ItemAccessEffect string

const (
	// ItemAccessAllow shows the item and its descendants despite blocked
	// tags and the rating ceiling, within the library grants.
	ItemAccessAllow ItemAccessEffect = "allow"
	// ItemAccessHide hides the item and its descendants.
	ItemAccessHide ItemAccessEffect = "hide"
)

func (e ItemAccessEffect) Valid() bool { return e == ItemAccessAllow || e == ItemAccessHide }

// ContentAccess are the per-user restrictions an administrator replaces as
// a whole.
type ContentAccess struct {
	// ParentalRatingMax is the highest allowed rating level; nil (omitted)
	// means no ceiling.
	ParentalRatingMax *int `json:"parentalRatingMax,omitempty"`
	// BlockUnrated decides unrated items under a ceiling; nil (omitted)
	// follows the server-wide policy.
	BlockUnrated *bool `json:"blockUnrated,omitempty"`
	// BlockedTags hides items whose tags or genres, or their ancestors',
	// contain one of them, compared case-insensitively.
	BlockedTags []string `json:"blockedTags"`
}

// Valid bounds the ceiling and the blocked tags. Tags are compared after
// trimming; an empty or control-character tag is invalid.
func (c ContentAccess) Valid() bool {
	if c.ParentalRatingMax != nil && (*c.ParentalRatingMax < 0 || *c.ParentalRatingMax > ParentalRatingLevelMax) {
		return false
	}
	if len(c.BlockedTags) > ContentBlockedTagsMax {
		return false
	}
	for _, tag := range c.BlockedTags {
		trimmed := strings.TrimSpace(tag)
		if trimmed == "" || len(tag) > ContentBlockedTagMax || !utf8.ValidString(tag) || strings.IndexFunc(tag, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

// ItemAccessRule is one explicit rule with the item it names.
type ItemAccessRule struct {
	ItemID    string           `json:"itemId"`
	LibraryID string           `json:"libraryId"`
	Kind      string           `json:"kind"`
	Title     string           `json:"title"`
	Effect    ItemAccessEffect `json:"effect"`
	CreatedAt time.Time        `json:"createdAt"`
}

// ContentAccessView is a user's restrictions with their item rules.
type ContentAccessView struct {
	ContentAccess
	Rules []ItemAccessRule `json:"rules"`
}

// AccessPolicy is the server-wide content access policy.
type AccessPolicy struct {
	// RestrictAdmins applies the content rules to administrators too.
	// Library grants never restrict administrators.
	RestrictAdmins bool `json:"restrictAdmins"`
	// BlockUnrated is the default for unrated items under a ceiling.
	BlockUnrated bool `json:"blockUnrated"`
}

// ParentalRating is one recognized rating code and its level.
type ParentalRating struct {
	Code  string `json:"code"`
	Level int    `json:"level"`
}
