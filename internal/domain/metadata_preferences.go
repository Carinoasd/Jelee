package domain

type MetadataPreferences struct {
	LibraryID string `json:"libraryId"`
	Language  string `json:"language"`
	Revision  int64  `json:"revision"`
}

func ValidMetadataPreferenceUpdate(library, language string, expected int64) bool {
	return ValidID(library) && ValidMetadataLanguage(language) && expected > 0 && expected < 2147483647
}
