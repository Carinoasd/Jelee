package domain

import (
	"errors"
	"time"
)

var ErrMetadataUnavailable = errors.New("metadata unavailable")

// MovieCandidate is provider data for review. Fetching it never changes an item,
// a field lock, or an original NFO or image asset.
type MovieCandidate struct {
	ProviderID    int32     `json:"providerId"`
	Source        string    `json:"source"`
	SourceURL     string    `json:"sourceUrl"`
	Language      string    `json:"language"`
	FetchedAt     time.Time `json:"fetchedAt"`
	Title         string    `json:"title"`
	OriginalTitle string    `json:"originalTitle"`
	Overview      string    `json:"overview"`
	ReleaseDate   string    `json:"releaseDate"`
}

func ValidMetadataLanguage(language string) bool {
	switch language {
	case "zh-CN", "zh-TW", "ja-JP", "en-US":
		return true
	}
	return false
}
