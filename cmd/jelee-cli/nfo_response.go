package main

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func decodeCLINFOResponse(raw []byte, request nfoCLIRequest) (any, bool) {
	if !utf8.Valid(raw) {
		return nil, false
	}
	envelope, ok := jobsCLIObject(raw)
	if !ok || !jobsCLIHas(envelope, "data") {
		return nil, false
	}
	data := envelope["data"]
	var result any
	switch request.command {
	case "validate":
		value, valid := decodeCLIJob(data)
		if !valid || value.LibraryID != request.library {
			return nil, false
		}
		result = value
	case "policy-get", "policy-set":
		var value domain.NFOLibraryPolicy
		if !jobsCLIPublicObject(data, &value, []string{"libraryId", "mode", "generation"}) ||
			domain.ValidateNFOLibraryPolicy(value) != nil || value.LibraryID != request.library ||
			request.command == "policy-set" && (value.Mode != request.mode || value.Generation != request.expected && value.Generation != request.expected+1) {
			return nil, false
		}
		result = value
	case "job":
		var value domain.NFOJobSummary
		if !jobsCLIPublicObject(data, &value, []string{"jobId", "libraryId", "mode", "phase", "processed", "hits", "negativeHits", "parsed", "valid", "invalid", "warningFiles", "changed", "unavailable", "rejected"}, "errorCode") ||
			domain.ValidateNFOJobSummary(value) != nil || value.JobID != request.id {
			return nil, false
		}
		result = value
	case "images":
		var value domain.ImageJobSummary
		if !jobsCLIPublicObject(data, &value, []string{"jobId", "libraryId", "added", "changed", "unchanged", "missing", "uncompared", "comparisonComplete"}) ||
			domain.ValidateImageJobSummary(value) != nil || value.JobID != request.id {
			return nil, false
		}
		result = value
	case "current-validations":
		value, valid := decodeCLINFOObservations(data, request)
		if !valid {
			return nil, false
		}
		result = value
	case "issues":
		value, valid := decodeCLINFOIssues(data, request)
		if !valid {
			return nil, false
		}
		result = value
	default:
		return nil, false
	}
	return map[string]any{"data": result}, true
}

func decodeCLINFOObservations(raw []byte, request nfoCLIRequest) (domain.NFOObservationPage, bool) {
	var wire struct {
		Items      []json.RawMessage `json:"items"`
		NextCursor string            `json:"nextCursor"`
	}
	if !jobsCLIPublicObject(raw, &wire, []string{"items"}, "nextCursor") || wire.Items == nil || len(wire.Items) > request.limit {
		return domain.NFOObservationPage{}, false
	}
	result := domain.NFOObservationPage{Items: make([]domain.NFOObservation, 0, len(wire.Items)), NextCursor: wire.NextCursor}
	for _, item := range wire.Items {
		var value domain.NFOObservation
		if !jobsCLIPublicObject(item, &value, []string{"id", "rootId", "path", "status", "entries", "failureCode", "warningCount", "errorCount", "issueCount", "issuesTruncated", "observedAt", "expiresAt"}) ||
			domain.ValidateNFOObservation(value) != nil || request.cursor != "" && value.ID <= request.cursor {
			return domain.NFOObservationPage{}, false
		}
		result.Items = append(result.Items, value)
	}
	if domain.ValidateNFOObservationPage(result) != nil {
		return domain.NFOObservationPage{}, false
	}
	return result, true
}

func decodeCLINFOIssues(raw []byte, request nfoCLIRequest) (domain.NFOIssuesPage, bool) {
	var wire struct {
		ObservationID   string                `json:"observationId"`
		Entries         int                   `json:"entries"`
		FailureCode     domain.NFOFailureCode `json:"failureCode"`
		IssueCount      int64                 `json:"issueCount"`
		IssuesTruncated bool                  `json:"issuesTruncated"`
		Offset          int                   `json:"offset"`
		NextOffset      *int                  `json:"nextOffset"`
		Issues          []json.RawMessage     `json:"issues"`
	}
	if !jobsCLIPublicObject(raw, &wire, []string{"observationId", "entries", "failureCode", "issueCount", "issuesTruncated", "offset", "issues"}, "nextOffset") ||
		wire.Issues == nil || len(wire.Issues) > request.limit || wire.ObservationID != request.observation || wire.Offset != request.offset {
		return domain.NFOIssuesPage{}, false
	}
	result := domain.NFOIssuesPage{ObservationID: wire.ObservationID, Entries: wire.Entries, FailureCode: wire.FailureCode,
		IssueCount: wire.IssueCount, IssuesTruncated: wire.IssuesTruncated, Offset: wire.Offset, NextOffset: wire.NextOffset,
		Issues: make([]domain.NFOIssue, 0, len(wire.Issues))}
	for _, item := range wire.Issues {
		var value domain.NFOIssue
		if !jobsCLIPublicObject(item, &value, []string{"severity", "code", "field", "entry"}) {
			return domain.NFOIssuesPage{}, false
		}
		result.Issues = append(result.Issues, value)
	}
	if domain.ValidateNFOIssuesPage(result) != nil {
		return domain.NFOIssuesPage{}, false
	}
	return result, true
}
