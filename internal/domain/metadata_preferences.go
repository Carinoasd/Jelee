package domain

type MetadataPreferences struct {
	LibraryID      string   `json:"libraryId"`
	Language       string   `json:"language"`
	Revision       int64    `json:"revision"`
	ImageLanguages []string `json:"imageLanguages"`
}

func DefaultMetadataImageLanguages(preferred string) []string {
	switch preferred {
	case "en-US":
		return []string{"en", "null"}
	case "ja-JP":
		return []string{"ja", "en", "null"}
	default:
		return []string{"zh", "ja", "en", "null"}
	}
}

func ValidMetadataImageLanguages(languages []string) bool {
	if len(languages) < 1 || len(languages) > 4 {
		return false
	}
	seen := make(map[string]bool, len(languages))
	for _, language := range languages {
		if seen[language] || (language != "zh" && language != "ja" && language != "en" && language != "null") {
			return false
		}
		seen[language] = true
	}
	return true
}

func ValidMetadataImagePreferenceUpdate(options [][]string) bool {
	return len(options) == 0 || (len(options) == 1 && ValidMetadataImageLanguages(options[0]))
}

func ValidMetadataPreferenceUpdate(library, language string, expected int64) bool {
	return ValidID(library) && ValidMetadataLanguage(language) && expected > 0 && expected < 2147483647
}
