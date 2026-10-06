package httpapi

import (
	"net/http"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/go-chi/chi/v5"
)

// Self-healing repair actions (G50.4) for administrators. A dry run lists
// the affected objects and their number and writes nothing; an execution
// needs iUnderstand, applies the same plan, records a repair run and audits
// it. docs/repair.md describes the actions; `jelee-cli repair` runs them on
// the host without a session.

// WithRepair mounts the repair routes. template carries the configured
// bounds; action, library, dry run, origin and actor are set per request.
func WithRepair(r *app.Repairer, template app.RepairOptions) Option {
	return func(s *Server) { s.repair, s.repairTemplate = r, template }
}

// repairRequest is the body of POST /api/v1/admin/repairs.
type repairRequest struct {
	Action      string `json:"action"`
	LibraryID   string `json:"libraryId"`
	DryRun      bool   `json:"dryRun"`
	IUnderstand bool   `json:"iUnderstand"`
	StatBudget  int    `json:"statBudget"`
}

func (s *Server) repairRoutes(r chi.Router) {
	r.Post("/api/v1/admin/repairs", s.accountEndpoint(true, false, s.runRepair))
	r.Post("/api/v1/admin/repairs/{id}/revert", s.accountEndpoint(true, false, s.revertRepair))
}

// errRepairUnwired answers when the process did not wire the repairer; the
// runtime always does with jobs and accounts enabled.
var errRepairUnwired = domain.ErrDatabase

func (s *Server) runRepair(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	if s.repair == nil {
		return nil, 0, errRepairUnwired
	}
	var input repairRequest
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	if !domain.ValidRepairAction(input.Action) || input.LibraryID != "" && !domain.ValidID(input.LibraryID) || input.StatBudget < 0 || input.StatBudget > domain.ConsistencyMaxStatBudget {
		return nil, 0, domain.ErrInvalid
	}
	// G45.6: an execution changes or discards data and needs an explicit
	// acknowledgement; a dry run does not.
	if !input.DryRun && !input.IUnderstand {
		return nil, 0, errConfirmationRequired
	}
	options := s.repairTemplate
	options.Action, options.Library, options.DryRun = input.Action, input.LibraryID, input.DryRun
	options.Origin, options.Actor = domain.RepairOriginAPI, a
	if input.StatBudget > 0 {
		options.StatBudget = input.StatBudget
	}
	// An unavailable action carries the reason: 503 nfo_reader_unavailable
	// without the scan pipeline, 404 image_unavailable without an image store.
	result, err := s.repair.Run(r.Context(), options)
	return result, http.StatusOK, err
}

func (s *Server) revertRepair(w http.ResponseWriter, r *http.Request, a domain.Actor) (any, int, error) {
	if s.repair == nil {
		return nil, 0, errRepairUnwired
	}
	var input struct {
		IUnderstand bool `json:"iUnderstand"`
	}
	if err := DecodeJSON(w, r, &input, accountBodyLimit); err != nil {
		return nil, 0, err
	}
	if !input.IUnderstand {
		return nil, 0, errConfirmationRequired
	}
	result, err := s.repair.Revert(r.Context(), a, chi.URLParam(r, "id"))
	return result, http.StatusOK, err
}
