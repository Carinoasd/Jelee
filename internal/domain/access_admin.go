package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Restricted time windows (G48.4) and the access administration of G48.7:
// the user × library grant matrix, bulk grant changes, templates and the
// preview of a change. docs/access-control.md describes them.

const (
	// AccessWindowsMax bounds the restricted time windows of a user.
	AccessWindowsMax = 20
	// AccessTimeZoneMax bounds an IANA time zone name in bytes.
	AccessTimeZoneMax = 64
	// AccessTemplatesMax bounds the stored access templates.
	AccessTemplatesMax = 100
	// AccessTemplateNameMax bounds a template name in characters.
	AccessTemplateNameMax = 64
	// AccessBulkUsersMax bounds the distinct users of one bulk change or
	// template application; the preview evaluates every item for each.
	AccessBulkUsersMax = 100
	// AccessBulkOperationsMax bounds the operations of one bulk change.
	AccessBulkOperationsMax = 200
	// AccessBulkLibrariesMax bounds the libraries of one operation or
	// template.
	AccessBulkLibrariesMax = 1000
	// AccessGrantMatrixUsersMax bounds the users the grant matrix lists.
	AccessGrantMatrixUsersMax = 1000
)

// AccessWindow is one restricted time window of a user. While the request
// time, read in TimeZone, falls inside it, RatingMax caps the user's rating
// ceiling; without RatingMax every item is hidden.
type AccessWindow struct {
	// Weekdays are 0 (Sunday) to 6 (Saturday), the day the window opens;
	// empty means every day.
	Weekdays []int `json:"weekdays"`
	// Start and End are HH:MM wall-clock times; End may be 24:00, and an
	// End at or before Start crosses midnight.
	Start string `json:"start"`
	End   string `json:"end"`
	// TimeZone is the IANA name the times are read in, such as Asia/Taipei.
	TimeZone string `json:"timeZone"`
	// RatingMax is the rating ceiling inside the window; nil hides every
	// item.
	RatingMax *int `json:"ratingMax,omitempty"`
}

// ParseAccessClock reads an HH:MM time as minutes after midnight; 24:00 is
// accepted only as an end.
func ParseAccessClock(s string, end bool) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	for _, b := range []byte(s[:2] + s[3:]) {
		if b < '0' || b > '9' {
			return 0, false
		}
	}
	h, m := int(s[0]-'0')*10+int(s[1]-'0'), int(s[3]-'0')*10+int(s[4]-'0')
	if m > 59 || h > 24 || h == 24 && (m != 0 || !end) {
		return 0, false
	}
	return h*60 + m, true
}

// AccessClock renders minutes after midnight as HH:MM.
func AccessClock(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

// Valid checks the window's fields. The time zone must load as an explicit
// IANA zone ("Local" is refused: it differs between hosts); storage also
// checks that PostgreSQL knows it.
func (w AccessWindow) Valid() bool {
	start, ok := ParseAccessClock(w.Start, false)
	if !ok {
		return false
	}
	end, ok := ParseAccessClock(w.End, true)
	if !ok || start == end || len(w.Weekdays) > 7 {
		return false
	}
	seen := 0
	for _, d := range w.Weekdays {
		if d < 0 || d > 6 || seen&(1<<d) != 0 {
			return false
		}
		seen |= 1 << d
	}
	if w.RatingMax != nil && (*w.RatingMax < 0 || *w.RatingMax > ParentalRatingLevelMax) {
		return false
	}
	if w.TimeZone == "" || w.TimeZone == "Local" || len(w.TimeZone) > AccessTimeZoneMax || !utf8.ValidString(w.TimeZone) || strings.IndexFunc(w.TimeZone, unicode.IsControl) >= 0 {
		return false
	}
	_, err := time.LoadLocation(w.TimeZone)
	return err == nil
}

// Contains reports whether t falls inside the window. It is the reference
// the storage filter's SQL is tested against; the filter itself decides in
// SQL with the request time.
func (w AccessWindow) Contains(t time.Time) bool {
	loc, err := time.LoadLocation(w.TimeZone)
	if err != nil {
		return false
	}
	start, _ := ParseAccessClock(w.Start, false)
	end, _ := ParseAccessClock(w.End, true)
	local := t.In(loc)
	m, day := local.Hour()*60+local.Minute(), int(local.Weekday())
	switch {
	case start < end:
		if m < start || m >= end {
			return false
		}
	case m >= start:
	case m < end:
		day = (day + 6) % 7 // the window opened the previous day
	default:
		return false
	}
	if len(w.Weekdays) == 0 {
		return true
	}
	for _, d := range w.Weekdays {
		if d == day {
			return true
		}
	}
	return false
}

// ValidAccessWindows checks a replacement window list.
func ValidAccessWindows(windows []AccessWindow) bool {
	if len(windows) > AccessWindowsMax {
		return false
	}
	for _, w := range windows {
		if !w.Valid() {
			return false
		}
	}
	return true
}

// AccessGrantMatrix is the user × library grant table of the matrix page.
type AccessGrantMatrix struct {
	Libraries []LibraryGrant          `json:"libraries"`
	Users     []AccessGrantMatrixUser `json:"users"`
	// Truncated is set when more than AccessGrantMatrixUsersMax users exist;
	// the first ones by name are listed.
	Truncated bool `json:"truncated"`
}

// AccessGrantMatrixUser is one row of the matrix.
type AccessGrantMatrixUser struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Admin       bool   `json:"admin"`
	Disabled    bool   `json:"disabled"`
	// LibraryIDs are the library grants; administrators see every library
	// whatever their grants.
	LibraryIDs []string `json:"libraryIds"`
}

// AccessGrantAction adds or removes library grants.
type AccessGrantAction string

// The bulk grant actions.
const (
	AccessGrantAdd    AccessGrantAction = "add"
	AccessGrantRemove AccessGrantAction = "remove"
)

// AccessGrantOperation grants or withdraws every library of LibraryIDs for
// every user of UserIDs.
type AccessGrantOperation struct {
	Action     AccessGrantAction `json:"action"`
	UserIDs    []string          `json:"userIds"`
	LibraryIDs []string          `json:"libraryIds"`
}

// ValidAccessGrantOperations checks a bulk change: 1 to
// AccessBulkOperationsMax operations, each naming at least one user and
// library by ID without repeats, at most AccessBulkUsersMax distinct users
// in total.
func ValidAccessGrantOperations(ops []AccessGrantOperation) bool {
	if len(ops) == 0 || len(ops) > AccessBulkOperationsMax {
		return false
	}
	users := map[string]bool{}
	for _, op := range ops {
		if op.Action != AccessGrantAdd && op.Action != AccessGrantRemove || len(op.UserIDs) == 0 || len(op.LibraryIDs) == 0 ||
			len(op.UserIDs) > AccessBulkUsersMax || len(op.LibraryIDs) > AccessBulkLibrariesMax || !distinctIDs(op.UserIDs) || !distinctIDs(op.LibraryIDs) {
			return false
		}
		for _, id := range op.UserIDs {
			users[id] = true
		}
	}
	return len(users) <= AccessBulkUsersMax
}

// ValidAccessUserIDs checks the users of a template application.
func ValidAccessUserIDs(ids []string) bool {
	return len(ids) > 0 && len(ids) <= AccessBulkUsersMax && distinctIDs(ids)
}

func distinctIDs(ids []string) bool {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !ValidID(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// AccessTemplateInput is the administrator-editable part of a template: the
// libraries a user is granted and the content restrictions, applied as a
// whole (G48.7).
type AccessTemplateInput struct {
	Name       string   `json:"name"`
	LibraryIDs []string `json:"libraryIds"`
	ContentAccess
}

// Valid checks the name, libraries and restrictions.
func (in AccessTemplateInput) Valid() bool {
	name := strings.TrimSpace(in.Name)
	if name == "" || name != in.Name || utf8.RuneCountInString(name) > AccessTemplateNameMax || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false
	}
	if len(in.LibraryIDs) > AccessBulkLibrariesMax || !distinctIDs(in.LibraryIDs) {
		return false
	}
	return in.ContentAccess.Valid()
}

// AccessTemplate is a stored template.
type AccessTemplate struct {
	ID string `json:"id"`
	AccessTemplateInput
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AccessUserChange is one user of a change preview: what changes in the
// user's settings and how many items become visible or hidden.
type AccessUserChange struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	// AddedLibraryIDs and RemovedLibraryIDs are the grant changes.
	AddedLibraryIDs   []string `json:"addedLibraryIds"`
	RemovedLibraryIDs []string `json:"removedLibraryIds"`
	// RestrictionsChanged is set when a template changes the user's rating
	// ceiling, unrated override, blocked tags or keywords.
	RestrictionsChanged bool `json:"restrictionsChanged"`
	// Shown and Hidden count the items whose visibility to the user changes.
	Shown  int64 `json:"shown"`
	Hidden int64 `json:"hidden"`
}

// AccessChangePreview reports what a bulk change or template application
// does (G48.7): the users whose settings change and the items whose
// visibility changes, counted with the unified filter at the request time,
// independent of the request's network. A preview writes nothing; an
// applied change returns the same counts and audits them.
type AccessChangePreview struct {
	Applied bool `json:"applied"`
	// Users counts users whose grants or restrictions change.
	Users int `json:"users"`
	// Items counts distinct items whose visibility changes for at least one
	// of the users; Shown and Hidden count user × item pairs.
	Items   int64              `json:"items"`
	Shown   int64              `json:"shown"`
	Hidden  int64              `json:"hidden"`
	Changes []AccessUserChange `json:"changes"`
}
