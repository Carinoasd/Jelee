package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

// G18 setup state (migration setup_state). Store implements
// app.SetupStateRepository. Every write is a compare-and-swap on the version
// column; a completed row is final, which a trigger enforces as a backstop.

// setupStateMaxBytes mirrors the table's CHECK on the encoded document.
const setupStateMaxBytes = 256 << 10

// SetupDatabaseFacts are the raw facts behind the wizard's database step.
type SetupDatabaseFacts struct {
	ServerVersion int
	SchemaVersion int
	Dirty         bool
}

// SetupDatabaseFacts reads the PostgreSQL server version and the migration
// state through the serving pool.
func (s *Store) SetupDatabaseFacts(ctx context.Context) (SetupDatabaseFacts, error) {
	var f SetupDatabaseFacts
	if err := s.Pool.QueryRow(ctx, `SELECT current_setting('server_version_num')::int`).Scan(&f.ServerVersion); err != nil {
		return f, storageError(err)
	}
	err := s.Pool.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations`).Scan(&f.SchemaVersion, &f.Dirty)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, nil
	}
	return f, storageError(err)
}

func (s *Store) LoadSetupState(ctx context.Context) (domain.SetupState, error) {
	return loadSetupState(ctx, s.Pool, false)
}

type setupQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadSetupState(ctx context.Context, q setupQuerier, lock bool) (domain.SetupState, error) {
	query := `SELECT version,current_step,state,completed_at FROM setup_state WHERE id=1`
	if lock {
		query += ` FOR UPDATE`
	}
	var (
		version   int64
		step      string
		document  []byte
		completed *time.Time
	)
	if err := q.QueryRow(ctx, query).Scan(&version, &step, &document, &completed); err != nil {
		return domain.SetupState{}, storageError(err)
	}
	var state domain.SetupState
	if err := json.Unmarshal(document, &state); err != nil {
		return domain.SetupState{}, domain.ErrDatabase
	}
	current, ok := domain.ParseSetupStep(step)
	if !ok {
		return domain.SetupState{}, domain.ErrDatabase
	}
	// Columns are authoritative over anything the document might carry.
	state.Version, state.Current, state.CompletedAt = version, current, nil
	if completed != nil {
		at := completed.UTC()
		state.CompletedAt = &at
	}
	return state, nil
}

func (s *Store) HasActiveAdmin(ctx context.Context) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE is_admin AND NOT disabled AND deleted_at IS NULL)`).Scan(&exists)
	return exists, storageError(err)
}

func (s *Store) SaveSetupState(ctx context.Context, next domain.SetupState) (domain.SetupState, error) {
	if next.Completed() {
		return domain.SetupState{}, domain.ErrInvalid
	}
	tx, err := s.setupTransaction(ctx)
	if err != nil {
		return domain.SetupState{}, err
	}
	defer tx.Rollback(ctx)
	before, err := lockSetupVersion(ctx, tx, next.Version)
	if err != nil {
		return domain.SetupState{}, err
	}
	stored, err := writeSetupState(ctx, tx, next, nil)
	if err != nil {
		return domain.SetupState{}, err
	}
	if err = auditSetup(ctx, tx, "setup.step_saved", "", map[string]any{"step": before}, map[string]any{"step": stored.Current.String()}); err != nil {
		return domain.SetupState{}, err
	}
	return stored, storageError(tx.Commit(ctx))
}

// CreateSetupAdmin creates the wizard's administrator and records it in the
// same transaction, serialized with every other account change.
func (s *Store) CreateSetupAdmin(ctx context.Context, input domain.UserInput, next domain.SetupState) (domain.SetupState, error) {
	if next.Completed() || next.Admin.UserID != "" {
		return domain.SetupState{}, domain.ErrInvalid
	}
	input.Admin, input.Disabled, input.Hidden = true, false, false
	in, err := normalizeUserInput(input, true)
	if err != nil {
		return domain.SetupState{}, err
	}
	tx, err := s.accountTransaction(ctx)
	if err != nil {
		return domain.SetupState{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockSetupVersion(ctx, tx, next.Version); err != nil {
		return domain.SetupState{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE is_admin AND NOT disabled AND deleted_at IS NULL)`).Scan(&exists); err != nil {
		return domain.SetupState{}, storageError(err)
	}
	if exists {
		return domain.SetupState{}, domain.ErrSetupAdminExists
	}
	user, err := insertUser(ctx, tx, in)
	if err != nil {
		return domain.SetupState{}, err
	}
	next.Admin.UserID = user.ID
	stored, err := writeSetupState(ctx, tx, next, nil)
	if err != nil {
		return domain.SetupState{}, err
	}
	if err = auditSetup(ctx, tx, "setup.admin_created", user.ID, nil, user); err != nil {
		return domain.SetupState{}, err
	}
	return stored, storageError(tx.Commit(ctx))
}

// CompleteSetup writes the initialization records in one transaction: it
// rechecks the wizard's administrator, creates the libraries with the chosen
// NFO and metadata language policy, and finalizes the state. Any failure,
// including a name or root that another writer took meanwhile, rolls back
// everything and leaves the wizard where it was.
func (s *Store) CompleteSetup(ctx context.Context, next domain.SetupState) (domain.SetupState, error) {
	if !next.Completed() || next.Current != domain.SetupStepComplete || !domain.ValidID(next.Admin.UserID) {
		return domain.SetupState{}, domain.ErrInvalid
	}
	language := next.TMDB.Language
	if !next.TMDB.Enabled {
		language = next.Locale
	}
	if !domain.ValidMetadataLanguage(language) || !domain.ValidNFOMode(next.MetadataPolicy.NFORead) {
		return domain.SetupState{}, domain.ErrInvalid
	}
	tx, err := s.accountTransaction(ctx)
	if err != nil {
		return domain.SetupState{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockJobs(ctx, tx); err != nil {
		return domain.SetupState{}, err
	}
	if _, err = lockSetupVersion(ctx, tx, next.Version); err != nil {
		return domain.SetupState{}, err
	}
	var admin bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1::uuid AND is_admin AND NOT disabled AND deleted_at IS NULL)`, next.Admin.UserID).Scan(&admin); err != nil {
		return domain.SetupState{}, storageError(err)
	}
	if !admin {
		return domain.SetupState{}, domain.ErrConflict
	}
	libraries := make([]domain.LibraryRegistration, 0, len(next.Media))
	for _, library := range next.Media {
		var r domain.LibraryRegistration
		// Plain inserts: an existing name or root is a conflict, never a merge.
		if err = tx.QueryRow(ctx, `INSERT INTO libraries(name,nfo_mode,metadata_language) VALUES($1,$2,$3) RETURNING id::text,name`, library.Name, next.MetadataPolicy.NFORead, language).Scan(&r.Library.ID, &r.Library.Name); err != nil {
			return domain.SetupState{}, storageError(err)
		}
		if err = tx.QueryRow(ctx, `INSERT INTO library_roots(library_id,path) VALUES($1::uuid,$2) RETURNING id::text`, r.Library.ID, library.Path).Scan(&r.RootID); err != nil {
			return domain.SetupState{}, storageError(err)
		}
		r.Library.Roots = 1
		if err = auditAccount(ctx, tx, setupActor(ctx), "library.registered", r.Library.ID, nil, r); err != nil {
			return domain.SetupState{}, err
		}
		libraries = append(libraries, r)
	}
	stored, err := writeSetupState(ctx, tx, next, next.CompletedAt)
	if err != nil {
		return domain.SetupState{}, err
	}
	summary := map[string]any{"libraries": len(libraries), "locale": next.Locale, "networkMode": next.Network.Mode, "tmdb": next.TMDB.Enabled, "nfoRead": next.MetadataPolicy.NFORead}
	if err = auditSetup(ctx, tx, "setup.completed", "", nil, summary); err != nil {
		return domain.SetupState{}, err
	}
	return stored, storageError(tx.Commit(ctx))
}

// setupTransaction bounds lock waits like account changes do. Steps that
// touch accounts or libraries use accountTransaction instead, which also
// serializes them with every other account change.
func (s *Store) setupTransaction(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='1500ms'; SET LOCAL statement_timeout='2000ms'`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, storageError(err)
	}
	return tx, nil
}

// lockSetupVersion locks the row and checks the caller's version: 0 requires
// that nothing was stored yet. It returns the stored current step name.
func lockSetupVersion(ctx context.Context, tx pgx.Tx, version int64) (string, error) {
	state, err := loadSetupState(ctx, tx, true)
	if errors.Is(err, domain.ErrNotFound) {
		if version != 0 {
			return "", domain.ErrConflict
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if state.Completed() || state.Version != version {
		return "", domain.ErrConflict
	}
	return state.Current.String(), nil
}

// writeSetupState inserts or updates the single row after lockSetupVersion.
func writeSetupState(ctx context.Context, tx pgx.Tx, next domain.SetupState, completed *time.Time) (domain.SetupState, error) {
	if !next.Current.Valid() {
		return domain.SetupState{}, domain.ErrInvalid
	}
	document := next
	document.Version, document.CompletedAt = 0, nil
	encoded, err := json.Marshal(document)
	if err != nil || len(encoded) > setupStateMaxBytes {
		return domain.SetupState{}, domain.ErrInvalid
	}
	var version int64
	if next.Version == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO setup_state(id,version,current_step,state,completed_at) VALUES(1,1,$1,$2::jsonb,$3) ON CONFLICT(id) DO NOTHING RETURNING version`,
			next.Current.String(), encoded, completed).Scan(&version)
	} else {
		err = tx.QueryRow(ctx, `UPDATE setup_state SET version=version+1,current_step=$2,state=$3::jsonb,completed_at=$4,updated_at=now() WHERE id=1 AND version=$1 AND completed_at IS NULL RETURNING version`,
			next.Version, next.Current.String(), encoded, completed).Scan(&version)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SetupState{}, domain.ErrConflict
	}
	if err != nil {
		return domain.SetupState{}, storageError(err)
	}
	return loadSetupState(ctx, tx, false)
}

func setupActor(ctx context.Context) domain.Actor {
	return domain.Actor{IP: domain.SetupOriginFrom(ctx).IP}
}

func auditSetup(ctx context.Context, tx pgx.Tx, event, target string, before, after any) error {
	origin := domain.SetupOriginFrom(ctx)
	if m, ok := after.(map[string]any); ok && origin.Channel != "" {
		m["channel"] = origin.Channel
	}
	entry := AuditEntry{Event: event, Actor: setupActor(ctx), TargetID: target, RequestID: origin.RequestID, Before: before, After: after}
	if target == "" {
		entry.TargetRef = "setup"
	}
	return appendAudit(ctx, tx, entry)
}
