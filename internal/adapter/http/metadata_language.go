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
