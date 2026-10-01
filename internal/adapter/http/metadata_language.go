package httpapi

import (
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func metadataRequestLanguage(r *http.Request, query map[string]string) string {
	if value, specified := query["language"]; specified {
		return value
	}
	principal, _ := access.PrincipalFromContext(r.Context())
	if domain.ValidMetadataLanguage(principal.Locale) {
		return principal.Locale
	}
	return "zh-CN"
}

func (s *Server) metadataLibraryLanguage(r *http.Request, query map[string]string, actor domain.Actor) (string, error) {
	language := metadataRequestLanguage(r, query)
	if !domain.ValidMetadataLanguage(language) {
		return "", domain.ErrInvalid
	}
	if library, exists := query["libraryId"]; exists {
		preferences, err := s.metadata.LibraryPreferences(r.Context(), actor, library)
		if err != nil {
			return "", err
		}
		if _, overridden := query["language"]; !overridden {
			language = preferences.Language
		}
	}
	return language, nil
}
