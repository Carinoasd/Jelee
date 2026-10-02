package httpapi

import "github.com/MoYuanCN/Jelee/internal/domain"

func nfoItemObservationSpecification(schemas map[string]any) {
	digest := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"} }
	statuses := []string{domain.NFOItemObservedValid, domain.NFOItemObservedMissing, domain.NFOItemObservedInvalid}
	schemas["NFOItemObservationStamp"] = objectSchema(map[string]any{
		"size":             map[string]any{"type": "integer", "minimum": 0, "maximum": domain.NFOMaxSourceBytes},
		"modifiedUnixNano": map[string]any{"type": "integer", "format": "int64"},
		"sha256":           digest(), "fingerprintVersion": map[string]any{"type": "string", "const": domain.NFOFingerprintVersion},
	}, "size", "modifiedUnixNano", "sha256", "fingerprintVersion")
	schemas["LastConfirmedNFOObservation"] = objectSchema(map[string]any{
		"version":        map[string]any{"type": "string", "const": domain.NFOItemObservationVersion},
		"status":         map[string]any{"type": "string", "enum": statuses},
		"sourceId":       map[string]any{"type": "string", "format": "uuid"},
		"rootId":         map[string]any{"type": "string", "format": "uuid"},
		"generation":     map[string]any{"type": "integer", "format": "int64", "minimum": 1},
		"identityDigest": digest(), "candidateDigest": digest(),
		"readAt":           map[string]any{"type": "string", "format": "date-time"},
		"acceptedRevision": map[string]any{"type": "integer", "minimum": 2, "maximum": domain.ItemMetadataRevisionMax},
		"stamp":            map[string]any{"oneOf": []any{map[string]any{"$ref": "#/components/schemas/NFOItemObservationStamp"}, map[string]any{"type": "null"}}},
	}, "version", "status", "sourceId", "rootId", "generation", "identityDigest", "candidateDigest", "readAt", "acceptedRevision", "stamp")
	observation := schemas["LastConfirmedNFOObservation"].(map[string]any)
	observation["description"] = "Last accepted NFO review; historical source/root identity and revision, not current filesystem freshness. Missing has a null stamp; valid or nfo_invalid retains original-byte proof. No local paths or field values."
	observation["allOf"] = []any{map[string]any{
		"if":   map[string]any{"properties": map[string]any{"status": map[string]any{"const": domain.NFOItemObservedMissing}}},
		"then": map[string]any{"properties": map[string]any{"stamp": map[string]any{"type": "null"}, "candidateDigest": map[string]any{"const": domain.NFOCandidateDigest([]string{})}}},
		"else": map[string]any{"properties": map[string]any{"stamp": map[string]any{"$ref": "#/components/schemas/NFOItemObservationStamp"}, "candidateDigest": map[string]any{"not": map[string]any{"const": domain.NFOCandidateDigest([]string{})}}}},
	}}
	item := schemas["ItemMetadata"].(map[string]any)
	item["properties"].(map[string]any)["lastConfirmedNFOObservation"] = map[string]any{"oneOf": []any{map[string]any{"$ref": "#/components/schemas/LastConfirmedNFOObservation"}, map[string]any{"type": "null"}}}
	item["required"] = append(item["required"].([]string), "lastConfirmedNFOObservation")
}
