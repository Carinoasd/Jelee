package domain

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidNFOObservationPath validates the public root-relative slash path. It
// accepts Unicode filenames but no controls, device/drive syntax or traversal.
func ValidNFOObservationPath(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > ScanPathMaxBytes || !utf8.ValidString(value) ||
		strings.ContainsAny(value, `\:`) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "../") ||
		path.Clean(value) != value || strings.Count(value, "/") >= 128 {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func ValidateNFOObservation(v NFOObservation) error {
	if !ValidID(v.ID) || !ValidID(v.RootID) || !ValidNFOObservationPath(v.Path) ||
		(v.Status != NFOStatusValid && v.Status != NFOStatusInvalid) || v.Entries < 0 || v.Entries > NFOEntriesMax ||
		v.WarningCount < 0 || v.WarningCount > NFOIssueTotalMax || v.ErrorCount < 0 || v.ErrorCount > NFOIssueTotalMax ||
		v.IssueCount < 0 || v.IssueCount > NFOIssueTotalMax || v.IssueCount != v.WarningCount+v.ErrorCount ||
		v.IssuesTruncated != (v.IssueCount > NFOIssuesMax) ||
		v.ObservedAt.IsZero() || v.ExpiresAt.IsZero() || v.ObservedAt.Year() < 1 || v.ExpiresAt.Year() > 9999 ||
		!v.ExpiresAt.After(v.ObservedAt) {
		return ErrInvalid
	}
	if v.FailureCode != "" {
		if !ValidNFOParseFailureCode(v.FailureCode) || v.Status != NFOStatusInvalid || v.Entries != 0 || v.IssueCount != 0 {
			return ErrInvalid
		}
		return nil
	}
	if v.Entries < 1 || (v.Status == NFOStatusInvalid) != (v.ErrorCount > 0) {
		return ErrInvalid
	}
	return nil
}

func ValidateNFOObservationPage(v NFOObservationPage) error {
	if v.Items == nil || len(v.Items) > NFOObservationPageMax {
		return ErrInvalid
	}
	for i, observation := range v.Items {
		if ValidateNFOObservation(observation) != nil || i > 0 && observation.ID <= v.Items[i-1].ID {
			return ErrInvalid
		}
	}
	if v.NextCursor != "" && (len(v.Items) == 0 || v.NextCursor != v.Items[len(v.Items)-1].ID) {
		return ErrInvalid
	}
	return nil
}

func ValidateNFOIssuesPage(v NFOIssuesPage) error {
	if !ValidID(v.ObservationID) || v.Entries < 0 || v.Entries > NFOEntriesMax ||
		v.IssueCount < 0 || v.IssueCount > NFOIssueTotalMax || v.IssuesTruncated != (v.IssueCount > NFOIssuesMax) ||
		v.Offset < 0 || v.Offset > NFOIssuesMax || v.Issues == nil || len(v.Issues) > NFOIssuesPageMax {
		return ErrInvalid
	}
	retained := int(min(v.IssueCount, NFOIssuesMax))
	end := v.Offset + len(v.Issues)
	if end > retained || v.NextOffset == nil && end != retained ||
		v.NextOffset != nil && (len(v.Issues) == 0 || *v.NextOffset != end || end >= retained) {
		return ErrInvalid
	}
	if v.FailureCode != "" {
		if !ValidNFOParseFailureCode(v.FailureCode) || v.Entries != 0 || v.IssueCount != 0 {
			return ErrInvalid
		}
		return nil
	}
	if v.Entries == 0 {
		return ErrInvalid
	}
	guessed := false
	for _, issue := range v.Issues {
		if !ValidNFOIssue(issue) || issue.Entry >= v.Entries {
			return ErrInvalid
		}
		if issue.Code == "nfo_encoding_guessed" {
			if guessed {
				return ErrInvalid
			}
			guessed = true
		}
	}
	return nil
}
