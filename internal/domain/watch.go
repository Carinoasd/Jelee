package domain

import "time"

const MaxWatchLibraries = 8

// WatchLease and its roots are private service data, never accepted from HTTP.
type WatchLease struct {
	LibraryID           string
	Owner               string
	Generation          int64
	Revision            int64
	InventoryGeneration int64
	ExpiresAt           time.Time
	Roots               []ScanDirectory
}

func (WatchLease) String() string   { return "watch lease (data redacted)" }
func (WatchLease) GoString() string { return "watch lease (data redacted)" }

type WatchStatus struct {
	LibraryID string `json:"libraryId"`
	Enabled   bool   `json:"enabled"`
	Observing bool   `json:"observing"`
	Pending   bool   `json:"pending"`
	LastJobID string `json:"lastJobId,omitempty"`
	LastError string `json:"lastError,omitempty"`
}
