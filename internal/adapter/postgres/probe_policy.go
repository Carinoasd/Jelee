package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/jackc/pgx/v5"
)

const probeDBTimeout = 2 * time.Second

// All probe writes share the existing jobs advisory lock. Filesystem access,
// hashing and processes are never performed inside these bounded transactions.
func (s *Store) probeTransaction(parent context.Context) (context.Context, context.CancelFunc, pgx.Tx, error) {
	if parent == nil {
		return nil, func() {}, nil, domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		cancel()
		return ctx, func() {}, nil, storageError(err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='1500ms'; SET LOCAL statement_timeout='2000ms'`); err == nil {
		err = lockJobs(ctx, tx)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		cancel()
		return ctx, func() {}, nil, storageError(err)
	}
	return ctx, cancel, tx, nil
}

const probePolicyColumns = `global_row_limit,global_byte_limit,library_row_limit,library_byte_limit,library_limit,tool_limit,active_lease_limit,positive_ttl_seconds,negative_ttl_seconds,lease_seconds`

func readProbePolicy(ctx context.Context, tx pgx.Tx) (domain.ProbeCachePolicy, error) {
	var p domain.ProbeCachePolicy
	var positive, negative, lease int64
	err := tx.QueryRow(ctx, `SELECT `+probePolicyColumns+` FROM probe_cache_quota WHERE singleton FOR UPDATE`).Scan(&p.MaxRows, &p.MaxBytes, &p.LibraryMaxRows, &p.LibraryMaxBytes, &p.MaxLibraries, &p.MaxToolVersions, &p.MaxLeases, &positive, &negative, &lease)
	if err != nil {
		return p, storageError(err)
	}
	p.PositiveTTL = time.Duration(positive) * time.Second
	p.NegativeTTL = time.Duration(negative) * time.Second
	p.LeaseDuration = time.Duration(lease) * time.Second
	return p, nil
}
func (s *Store) EnsureProbePolicy(parent context.Context, p domain.ProbeCachePolicy) error {
	if err := domain.ValidateProbeCachePolicy(p); err != nil {
		return err
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO probe_cache_quota(singleton,`+probePolicyColumns+`) VALUES(true,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(singleton) DO NOTHING`, p.MaxRows, p.MaxBytes, p.LibraryMaxRows, p.LibraryMaxBytes, p.MaxLibraries, p.MaxToolVersions, p.MaxLeases, int64(p.PositiveTTL/time.Second), int64(p.NegativeTTL/time.Second), int64(p.LeaseDuration/time.Second))
	if err != nil {
		return storageError(err)
	}
	existing, err := readProbePolicy(ctx, tx)
	if err != nil {
		return err
	}
	if existing != p {
		return domain.ErrConflict
	}
	return storageError(tx.Commit(ctx))
}
func probeBytes(value string) []byte { b, _ := hex.DecodeString(value); return b }

const probeIdentityColumns = `platform,vendor_version,upstream_version,source_revision,encode(executable_sha256,'hex'),encode(runtime_sha256,'hex'),parser_version,metadata_schema_version,encode(arguments_sha256,'hex'),sandbox_version,fingerprint_version`

func scanProbeIdentity(row pgx.Row) (domain.ProbeIdentity, error) {
	var in domain.ProbeIdentity
	err := row.Scan(&in.Platform, &in.VendorVersion, &in.UpstreamVersion, &in.SourceRevision, &in.ExecutableSHA256, &in.RuntimeSHA256, &in.ParserVersion, &in.MetadataSchemaVersion, &in.ArgumentsSHA256, &in.SandboxVersion, &in.FingerprintVersion)
	return in, storageError(err)
}
func (s *Store) RegisterProbeIdentity(parent context.Context, in domain.ProbeIdentity) (domain.ProbeIdentityRef, error) {
	digest, err := domain.ProbeIdentityDigest(in)
	if err != nil {
		return domain.ProbeIdentityRef{}, err
	}
	ctx, cancel, tx, err := s.probeTransaction(parent)
	if err != nil {
		return domain.ProbeIdentityRef{}, err
	}
	defer cancel()
	defer tx.Rollback(ctx)
	if _, err = readProbePolicy(ctx, tx); err != nil {
		return domain.ProbeIdentityRef{}, err
	}
	ref := domain.ProbeIdentityRef{Digest: digest}
	err = tx.QueryRow(ctx, `SELECT id::text FROM tool_versions WHERE identity_digest=$1`, probeBytes(digest)).Scan(&ref.ID)
	if err == nil {
		existing, e := scanProbeIdentity(tx.QueryRow(ctx, `SELECT `+probeIdentityColumns+` FROM tool_versions WHERE id=$1::uuid`, ref.ID))
		if e != nil {
			return domain.ProbeIdentityRef{}, e
		}
		if existing != in {
			return domain.ProbeIdentityRef{}, domain.ErrProbeIdentityMismatch
		}
	} else if errors.Is(err, pgx.ErrNoRows) {
		tag, e := tx.Exec(ctx, `UPDATE probe_cache_quota SET tools_used=tools_used+1 WHERE singleton AND tools_used<tool_limit`)
		if e != nil {
			return domain.ProbeIdentityRef{}, storageError(e)
		}
		if tag.RowsAffected() != 1 {
			return domain.ProbeIdentityRef{}, domain.ErrProbeCacheCapacity
		}
		err = tx.QueryRow(ctx, `INSERT INTO tool_versions(identity_digest,platform,vendor_version,upstream_version,source_revision,executable_sha256,runtime_sha256,parser_version,metadata_schema_version,arguments_sha256,sandbox_version,fingerprint_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id::text`, probeBytes(digest), in.Platform, in.VendorVersion, in.UpstreamVersion, in.SourceRevision, probeBytes(in.ExecutableSHA256), probeBytes(in.RuntimeSHA256), in.ParserVersion, in.MetadataSchemaVersion, probeBytes(in.ArgumentsSHA256), in.SandboxVersion, in.FingerprintVersion).Scan(&ref.ID)
		if err != nil {
			return domain.ProbeIdentityRef{}, storageError(err)
		}
	} else {
		return domain.ProbeIdentityRef{}, storageError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProbeIdentityRef{}, storageError(err)
	}
	return ref, nil
}

func ensureProbeLibrary(ctx context.Context, tx pgx.Tx, library string) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM probe_library_quota WHERE library_id=$1::uuid)`, library).Scan(&exists); err != nil {
		return storageError(err)
	}
	if exists {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE probe_cache_quota SET library_scopes=library_scopes+1 WHERE singleton AND library_scopes<library_limit`)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrProbeCacheCapacity
	}
	_, err = tx.Exec(ctx, `INSERT INTO probe_library_quota(library_id,row_limit,byte_limit) SELECT $1::uuid,library_row_limit,library_byte_limit FROM probe_cache_quota WHERE singleton`, library)
	return storageError(err)
}

// Changes are always exact deltas, including provisional maximum payload space.
// The global singleton serializes limits shared by every service instance.
func adjustProbeQuota(ctx context.Context, tx pgx.Tx, library string, rows, bytes, leases int64) error {
	tag, err := tx.Exec(ctx, `UPDATE probe_cache_quota SET rows_used=rows_used+$1,bytes_used=bytes_used+$2,active_leases=active_leases+$3 WHERE singleton AND rows_used+$1 BETWEEN 0 AND global_row_limit AND bytes_used+$2 BETWEEN 0 AND global_byte_limit AND active_leases+$3 BETWEEN 0 AND active_lease_limit`, rows, bytes, leases)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrProbeCacheCapacity
	}
	tag, err = tx.Exec(ctx, `UPDATE probe_library_quota SET rows_used=rows_used+$2,bytes_used=bytes_used+$3,active_leases=active_leases+$4 WHERE library_id=$1::uuid AND rows_used+$2 BETWEEN 0 AND row_limit AND bytes_used+$3 BETWEEN 0 AND byte_limit AND active_leases+$4 BETWEEN 0 AND 8`, library, rows, bytes, leases)
	if err != nil {
		return storageError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrProbeCacheCapacity
	}
	return nil
}
