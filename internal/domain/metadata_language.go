package domain

import "time"

// RequestedLanguage records the provider request, not a guess about the
// language of the returned text. An empty source means no overview was found.
type MetadataFieldSource struct {
	RequestedLanguage string    `json:"requestedLanguage"`
	FetchedAt         time.Time `json:"fetchedAt"`
}

func MetadataFallbackLanguages(preferred string) []string {
	chain := []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}
	for i, value := range chain {
		if value == preferred {
			return chain[i:]
		}
	}
	return nil
}
