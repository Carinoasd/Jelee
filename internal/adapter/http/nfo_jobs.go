package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

type IgnoreRequestBody struct {
	Mode     string `json:"mode"`
	CaseMode string `json:"caseMode"`
}
type ScanRequestBody struct {
	Ignore   *IgnoreRequestBody `json:"ignore,omitempty"`
	Priority string             `json:"priority"`
	Probe    bool               `json:"probe"`
	NFO      bool               `json:"nfo"`
}

// WithNFOWritePreparations mounts item NFO write preparation. Without it the
// route answers 503 nfo_reader_unavailable.
func WithNFOWritePreparations(p *app.NFOWritePreparations) Option {
	return func(s *Server) { s.nfoWrites = p }
}

func (s *Server) nfoRoutes(r chi.Router) {
	r.Get("/api/v1/libraries/{id}/nfo/policy", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		policy, err := s.jobs.NFOPolicy(r.Context(), a, chi.URLParam(r, "id"))
		return policy, http.StatusOK, err
	}))
	r.Put("/api/v1/libraries/{id}/nfo/policy", s.accountEndpoint(true, false, s.setNFOPolicy))
	r.Put("/api/v1/libraries/{id}/nfo/write-policy", s.accountEndpoint(true, false, s.setNFOWritePolicy))
	r.Post("/api/v1/items/{id}/nfo/write-preparations", s.accountEndpoint(true, false, s.prepareNFOWrite))
	r.Post("/api/v1/libraries/{id}/nfo/writes", s.accountEndpoint(true, false, s.submitNFOWrite))
	r.Post("/api/v1/libraries/{id}/nfo/validate", s.accountEndpoint(true, false, s.validateLibraryNFO))
	r.Post("/api/v1/libraries/{id}/nfo/apply", s.accountEndpoint(true, false, s.applyLibraryNFO))
	r.Get("/api/v1/jobs/{id}/nfo-apply", s.accountEndpoint(true, false, func(_ http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		state, err := s.jobs.NFOApplyState(r.Context(), a, chi.URLParam(r, "id"))
		return state, http.StatusOK, err
	}))
	r.Get("/api/v1/jobs/{id}/nfo", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		summary, err := s.jobs.NFOSummary(r.Context(), a, chi.URLParam(r, "id"))
		return summary, http.StatusOK, err
	}))
	r.Get("/api/v1/jobs/{id}/images", s.accountEndpoint(true, false, func(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
		summary, err := s.jobs.Images(r.Context(), a, chi.URLParam(r, "id"))
		return summary, http.StatusOK, err
	}))
	r.Get("/api/v1/libraries/{id}/nfo/current-validations", s.accountEndpoint(true, true, s.listNFOObservations))
	r.Get("/api/v1/libraries/{id}/nfo/current-validations/{observationId}/issues", s.accountEndpoint(true, true, s.listNFOIssues))
}

func (s *Server) setNFOPolicy(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	var input struct {
		Mode               string `json:"mode"`
		ExpectedGeneration int64  `json:"expectedGeneration"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	key, err := jobKey(r)
	if err != nil {
		return nil, 0, err
	}
	policy, replay, err := s.jobs.SetNFOPolicy(r.Context(), a, chi.URLParam(r, "id"), key, input.ExpectedGeneration, input.Mode)
	if err == nil && replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	return policy, http.StatusOK, err
}

func (s *Server) validateLibraryNFO(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	var input struct {
		Priority string `json:"priority"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	key, err := jobKey(r)
	if err != nil {
		return nil, 0, err
	}
	if input.Priority == "" {
		input.Priority = domain.JobPriorityManual
	}
	job, replay, err := s.jobs.SubmitScanStages(r.Context(), a, chi.URLParam(r, "id"), key, input.Priority, false, true)
	return acceptedJob(w, job, replay, err)
}

// applyLibraryNFO queues a batch NFO apply (G39.14). The library must be
// read-only, otherwise the answer is 409 conflict.
func (s *Server) applyLibraryNFO(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	var input struct {
		Priority string `json:"priority"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	key, err := jobKey(r)
	if err != nil {
		return nil, 0, err
	}
	if input.Priority == "" {
		input.Priority = domain.JobPriorityManual
	}
	job, replay, err := s.jobs.SubmitNFOApply(r.Context(), a, chi.URLParam(r, "id"), key, input.Priority)
	return acceptedJob(w, job, replay, err)
}

func (s *Server) listNFOObservations(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	q, err := listQuery(r, "/api/v1/libraries/{id}/nfo/current-validations")
	if err != nil {
		return nil, 0, err
	}
	rows, next, err := keysetPage(q, func(cursor string, limit int) ([]domain.NFOObservation, string, error) {
		page, err := s.jobs.NFOObservations(r.Context(), a, chi.URLParam(r, "id"), cursor, limit)
		return page.Items, page.NextCursor, err
	})
	if err != nil {
		return nil, 0, err
	}
	return listData(q, domain.NFOObservationPage{Items: rows, NextCursor: next})
}

func (s *Server) listNFOIssues(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	query, err := strictQuery(r, "offset", "limit")
	if err != nil {
		return nil, 0, err
	}
	offset, limit := 0, domain.NFOIssuesPageMax
	if value, exists := query["offset"]; exists {
		offset, err = strconv.Atoi(value)
		if err != nil {
			return nil, 0, domain.ErrInvalid
		}
	}
	if value, exists := query["limit"]; exists {
		limit, err = strconv.Atoi(value)
		if err != nil {
			return nil, 0, domain.ErrInvalid
		}
	}
	page, err := s.jobs.NFOIssues(r.Context(), a, chi.URLParam(r, "id"), chi.URLParam(r, "observationId"), offset, limit)
	return page, http.StatusOK, err
}

// nfoWriteBackups is how many backups a preparation keeps when the request
// does not say.
const nfoWriteBackups = 3

func (s *Server) setNFOWritePolicy(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	if input.Enabled == nil {
		return nil, 0, domain.ErrInvalid
	}
	policy, err := s.jobs.SetNFOWritePolicy(r.Context(), a, chi.URLParam(r, "id"), *input.Enabled)
	return policy, http.StatusOK, err
}

// nfoWritePreparationSummary is the public shape of a preparation: counts and
// digests only, never file bytes, paths or roots.
type nfoWritePreparationSummary struct {
	ID                string    `json:"id"`
	ItemID            string    `json:"itemId"`
	LibraryID         string    `json:"libraryId"`
	Revision          int64     `json:"revision"`
	Fields            []string  `json:"fields"`
	OriginalBytes     int       `json:"originalBytes"`
	ReplacementBytes  int       `json:"replacementBytes"`
	OriginalSHA256    string    `json:"originalSha256"`
	ReplacementSHA256 string    `json:"replacementSha256"`
	CreatedAt         time.Time `json:"createdAt"`
	ExpiresAt         time.Time `json:"expiresAt"`
}

func summarizeNFOWritePreparation(p domain.NFOWritePreparation) nfoWritePreparationSummary {
	fields := make([]string, 0, len(p.Request.Edits))
	for _, edit := range p.Request.Edits {
		fields = append(fields, edit.Field)
	}
	digest := sha256.Sum256(p.Replacement)
	return nfoWritePreparationSummary{ID: p.ID, ItemID: p.Request.ItemID, LibraryID: p.Scope.LibraryID, Revision: p.Request.Revision, Fields: fields,
		OriginalBytes: len(p.Original), ReplacementBytes: len(p.Replacement), OriginalSHA256: p.Stamp.SHA256, ReplacementSHA256: hex.EncodeToString(digest[:]),
		CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt}
}

func (s *Server) prepareNFOWrite(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	var input struct {
		Revision int64                     `json:"revision"`
		Edits    []domain.NFOWriteTextEdit `json:"edits"`
		BOM      string                    `json:"bom"`
		Indent   string                    `json:"indent"`
		MaxBytes *int64                    `json:"maxBytes"`
		Backups  *int                      `json:"backups"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	key, err := jobKey(r)
	if err != nil {
		return nil, 0, err
	}
	// Missing-NFO creation is deliberately not exposed here.
	request := domain.NFOWritePrepareRequest{ItemID: chi.URLParam(r, "id"), Revision: input.Revision, Edits: input.Edits, BOM: input.BOM, Indent: input.Indent, MaxBytes: domain.NFODefaultSourceBytes, Backups: nfoWriteBackups}
	if input.Edits == nil {
		return nil, 0, domain.ErrInvalid
	}
	if input.MaxBytes != nil {
		request.MaxBytes = *input.MaxBytes
	}
	if input.Backups != nil {
		request.Backups = *input.Backups
	}
	if err = domain.ValidateNFOWritePrepareRequest(request); err != nil {
		return nil, 0, err
	}
	if s.nfoWrites == nil {
		return nil, 0, domain.ErrNFOReaderUnavailable
	}
	prepared, replay, err := s.nfoWrites.Prepare(r.Context(), a, key, request)
	if err != nil {
		return nil, 0, err
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
		w.Header().Set("Idempotency-Replayed", "true")
	}
	return summarizeNFOWritePreparation(prepared), status, nil
}

func (s *Server) submitNFOWrite(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	var input struct {
		Preparations []string `json:"preparations"`
		Priority     string   `json:"priority"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	key, err := jobKey(r)
	if err != nil {
		return nil, 0, err
	}
	if input.Priority == "" {
		input.Priority = domain.JobPriorityManual
	}
	job, replay, err := s.jobs.SubmitNFOWrite(r.Context(), a, chi.URLParam(r, "id"), key, input.Priority, input.Preparations)
	return acceptedJob(w, job, replay, err)
}
