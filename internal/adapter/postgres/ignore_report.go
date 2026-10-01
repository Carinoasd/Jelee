package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type ignoreReportCursor struct {
	Job    string `json:"job"`
	Source string `json:"source"`
	Root   string `json:"root"`
	Path   string `json:"path"`
}

func decodeIgnoreReportCursor(raw, id string) (ignoreReportCursor, error) {
	if raw == "" {
		return ignoreReportCursor{Job: id}, nil
	}
	if len(raw) > 4096 {
		return ignoreReportCursor{}, domain.ErrInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return ignoreReportCursor{}, domain.ErrInvalid
	}
	var c ignoreReportCursor
	if json.Unmarshal(data, &c) != nil || c.Job != id || (c.Source != "baseline" && c.Source != "scan") || !domain.ValidID(c.Root) || !domain.ValidNFOObservationPath(c.Path) {
		return ignoreReportCursor{}, domain.ErrInvalid
	}
	canonical, _ := json.Marshal(c)
	if base64.RawURLEncoding.EncodeToString(canonical) != raw {
		return ignoreReportCursor{}, domain.ErrInvalid
	}
	return c, nil
}

// CASE selects each source's cursor lower bound before the index scan. An OR
// between source and cursor predicates degenerates to filtering old rows when
// PostgreSQL chooses a generic prepared plan.
const ignoreReportPageSQL = `WITH b AS (
 SELECT 'baseline'::text source,root_id,path,''::text kind,outcome,rule_directory,rule_line,matched_path,reason,''::text family FROM job_ignore_decisions
 WHERE job_id=$1::uuid AND $2<='baseline' AND (root_id,path COLLATE "C")>(CASE WHEN $2='baseline' THEN NULLIF($3,'')::uuid ELSE '00000000-0000-0000-0000-000000000000'::uuid END,CASE WHEN $2='baseline' THEN $4 ELSE '' END COLLATE "C") ORDER BY root_id,path COLLATE "C" LIMIT $5
),s AS (
 SELECT 'scan'::text source,root_id,path,kind,'excluded'::text outcome,rule_directory,rule_line,matched_path,''::text reason,''::text family FROM job_ignore_exclusions
 WHERE job_id=$1::uuid AND (root_id,path COLLATE "C")>(CASE WHEN $2='scan' THEN NULLIF($3,'')::uuid ELSE '00000000-0000-0000-0000-000000000000'::uuid END,CASE WHEN $2='scan' THEN $4 ELSE '' END COLLATE "C") ORDER BY root_id,path COLLATE "C" LIMIT $5
)
 SELECT source,root_id::text,path,kind,outcome,rule_directory,rule_line,matched_path,reason,family FROM (SELECT * FROM b UNION ALL SELECT * FROM s) all_rows ORDER BY source COLLATE "C",root_id,path COLLATE "C" LIMIT $5`

const familyIgnoreReportPageSQL = `WITH b AS (
 SELECT 'baseline'::text source,root_id,path,''::text kind,outcome,rule_directory,rule_line,matched_path,reason,family FROM job_ignore_family_decisions
 WHERE job_id=$1::uuid AND $2<='baseline' AND (root_id,path COLLATE "C")>(CASE WHEN $2='baseline' THEN NULLIF($3,'')::uuid ELSE '00000000-0000-0000-0000-000000000000'::uuid END,CASE WHEN $2='baseline' THEN $4 ELSE '' END COLLATE "C") ORDER BY root_id,path COLLATE "C" LIMIT $5
),s AS (
 SELECT 'scan'::text source,root_id,path,kind,'excluded'::text outcome,rule_directory,rule_line,matched_path,reason,family FROM job_ignore_family_exclusions
 WHERE job_id=$1::uuid AND (root_id,path COLLATE "C")>(CASE WHEN $2='scan' THEN NULLIF($3,'')::uuid ELSE '00000000-0000-0000-0000-000000000000'::uuid END,CASE WHEN $2='scan' THEN $4 ELSE '' END COLLATE "C") ORDER BY root_id,path COLLATE "C" LIMIT $5
)
 SELECT source,root_id::text,path,kind,outcome,rule_directory,rule_line,matched_path,reason,family FROM (SELECT * FROM b UNION ALL SELECT * FROM s) all_rows ORDER BY source COLLATE "C",root_id,path COLLATE "C" LIMIT $5`

func (s *Store) GetIgnoreReport(parent context.Context, a domain.Actor, id string, limit int, cursor string) (domain.IgnoreReport, error) {
	empty := domain.IgnoreReport{}
	if parent == nil || limit < 1 || limit > 100 {
		return empty, domain.ErrInvalid
	}
	if !domain.ValidID(id) {
		return empty, domain.ErrNotFound
	}
	after, err := decodeIgnoreReportCursor(cursor, id)
	if err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(parent, probeDBTimeout)
	defer cancel()
	tx, err := s.authorizedJobs(ctx, a)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	out := domain.IgnoreReport{Entries: []domain.IgnoreReportEntry{}}
	err = tx.QueryRow(ctx, `SELECT j.id::text,j.state,j.ignore_requested,j.review_required,(COALESCE(m.invalidated,false) OR COALESCE(lm.invalidated,false)),COALESCE(s.excluded_files,0),COALESCE(s.excluded_directories,0),COALESCE(c.unknown,0) FROM jobs j LEFT JOIN job_ignore_manifests m ON m.job_id=j.id LEFT JOIN job_ignore_legacy_manifests lm ON lm.job_id=j.id LEFT JOIN job_ignore_scan_state s ON s.job_id=j.id LEFT JOIN job_ignore_comparisons c ON c.job_id=j.id WHERE j.id=$1::uuid`, id).Scan(&out.JobID, &out.State, &out.Enabled, &out.ReviewRequired, &out.Invalidated, &out.ExcludedFiles, &out.ExcludedDirectories, &out.Unknown)
	if err != nil {
		return empty, storageError(err)
	}
	if out.State == domain.JobQueued || out.State == domain.JobRunning {
		return empty, domain.ErrConflict
	}
	request, err := loadExecutionIgnoreRequest(ctx, tx, id, true)
	if err != nil {
		return empty, err
	}
	if out.Enabled != (request != nil) {
		return empty, domain.ErrConflict
	}
	pageSQL := ignoreReportPageSQL
	if request != nil && request.Intent.Mode == domain.IgnoreModeFamily {
		pageSQL = familyIgnoreReportPageSQL
	}
	rows, err := tx.Query(ctx, pageSQL, id, after.Source, after.Root, after.Path, limit+1)
	if err != nil {
		return empty, storageError(err)
	}
	for rows.Next() {
		var entry domain.IgnoreReportEntry
		if err = rows.Scan(&entry.Source, &entry.RootID, &entry.Path, &entry.Kind, &entry.Outcome, &entry.RuleDirectory, &entry.RuleLine, &entry.MatchedPath, &entry.Reason, &entry.Family); err != nil {
			rows.Close()
			return empty, storageError(err)
		}
		if domain.ValidateIgnoreReportEntry(entry) != nil {
			rows.Close()
			return empty, domain.ErrDatabase
		}
		out.Entries = append(out.Entries, entry)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return empty, storageError(err)
	}
	if len(out.Entries) > limit {
		out.Entries = out.Entries[:limit]
		last := out.Entries[limit-1]
		data, _ := json.Marshal(ignoreReportCursor{Job: id, Source: last.Source, Root: last.RootID, Path: last.Path})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	if err = probeAdminStillLive(ctx, tx, a); err != nil {
		return empty, err
	}
	if err = tx.Commit(ctx); err != nil {
		return empty, storageError(err)
	}
	return out, nil
}
