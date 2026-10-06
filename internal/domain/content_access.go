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
	// ContentBlockedKeywordsMax bounds the blocked keywords of a user.
	ContentBlockedKeywordsMax = 100
	// ContentBlockedKeywordMax bounds one blocked keyword in UTF-8 bytes.
	ContentBlockedKeywordMax = 128
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
	// BlockedKeywords hides items whose title, metadata title or original
	// title, or an ancestor's, contains one of them, compared after NFKC
	// normalization and in lower case (G48.4). nil (omitted) means none.
	BlockedKeywords []string `json:"blockedKeywords"`
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
		if !validContentTerm(tag, ContentBlockedTagMax) {
			return false
		}
	}
	if len(c.BlockedKeywords) > ContentBlockedKeywordsMax {
		return false
	}
	for _, keyword := range c.BlockedKeywords {
		if !validContentTerm(keyword, ContentBlockedKeywordMax) {
			return false
		}
	}
	return true
}

// validContentTerm accepts a blocked tag or keyword: not blank, valid UTF-8
// of at most limit bytes, without control characters.
func validContentTerm(term string, limit int) bool {
	return strings.TrimSpace(term) != "" && len(term) <= limit && utf8.ValidString(term) && strings.IndexFunc(term, unicode.IsControl) < 0
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

// ContentAccessView is a user's restrictions with their item rules and
// restricted time windows.
type ContentAccessView struct {
	ContentAccess
	Rules   []ItemAccessRule `json:"rules"`
	Windows []AccessWindow   `json:"windows"`
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

const (
	// ParentalRatingsMax bounds the rating code table.
	ParentalRatingsMax = 500
	// ParentalRatingCodeMax bounds one rating code in characters.
	ParentalRatingCodeMax = 32
)

// ValidParentalRatings checks a replacement rating table: at most
// ParentalRatingsMax codes of 1 to ParentalRatingCodeMax characters without
// control characters, levels 0 to ParentalRatingLevelMax. Storage
// normalizes the codes and refuses duplicates and codes the filter could
// never match.
func ValidParentalRatings(ratings []ParentalRating) bool {
	if len(ratings) > ParentalRatingsMax {
		return false
	}
	for _, r := range ratings {
		code := strings.TrimSpace(r.Code)
		if code == "" || utf8.RuneCountInString(r.Code) > ParentalRatingCodeMax || !utf8.ValidString(r.Code) || strings.IndexFunc(r.Code, unicode.IsControl) >= 0 ||
			r.Level < 0 || r.Level > ParentalRatingLevelMax {
			return false
		}
	}
	return true
}
