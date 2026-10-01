package domain

import "time"

const (
	NFOObservationPageDefault = 20
	NFOObservationPageMax     = 50
	NFOIssuesPageMax          = 32
)

// NFOObservation is the current unexpired database observation, not a fresh
// filesystem read or a historical job snapshot. Path is root-relative only.
type NFOObservation struct {
	ID              string         `json:"id"`
	RootID          string         `json:"rootId"`
	Path            string         `json:"path"`
	Status          string         `json:"status"`
	Entries         int            `json:"entries"`
	FailureCode     NFOFailureCode `json:"failureCode"`
	WarningCount    int64          `json:"warningCount"`
	ErrorCount      int64          `json:"errorCount"`
	IssueCount      int64          `json:"issueCount"`
	IssuesTruncated bool           `json:"issuesTruncated"`
	ObservedAt      time.Time      `json:"observedAt"`
	ExpiresAt       time.Time      `json:"expiresAt"`
}

type NFOObservationPage struct {
	Items      []NFOObservation `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// Offset is the number of retained issues already consumed, not an XML entry
// index. NextOffset is absent after the retained prefix (at most 64 issues).
type NFOIssuesPage struct {
	ObservationID   string         `json:"observationId"`
	Entries         int            `json:"entries"`
	FailureCode     NFOFailureCode `json:"failureCode"`
	IssueCount      int64          `json:"issueCount"`
	IssuesTruncated bool           `json:"issuesTruncated"`
	Offset          int            `json:"offset"`
	NextOffset      *int           `json:"nextOffset,omitempty"`
	Issues          []NFOIssue     `json:"issues"`
}
