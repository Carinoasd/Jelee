package postgres

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestFamilyReportTerminalPaginationAndAuthorization(t *testing.T) {
	f, l := familyComparisonFixture(t)
	var rootPath string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&rootPath); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 7, ""); err != domain.ErrConflict {
		t.Fatal("mutable report exposed", err)
	}
	classifyFamilyForPublication(t, f, l, func(e *domain.FamilyBaselineEvaluation) {
		*e = domain.FamilyBaselineEvaluation{Decision: domain.FamilyIgnoreBaselineDecision{RootID: e.Decision.RootID, Path: e.Decision.Path, Outcome: domain.IgnoreBaselineUnknown, Reason: domain.IgnoreUnknownSource}}
	}, false)
	if err := f.s.FinishFamilyIgnoreJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	firstCursor := ""
	baseline, custom, legacy := 0, 0, 0
	for page := 0; page < 30; page++ {
		r, err := f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 7, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Enabled || !r.ReviewRequired || r.Invalidated || r.Unknown != 130 || r.ExcludedFiles != 2 || r.ExcludedDirectories != 1 {
			t.Fatal("summary disagrees with retained classifications")
		}
		if len(r.Entries) == 0 || len(r.Entries) > 7 {
			t.Fatal("unbounded or empty page")
		}
		for _, e := range r.Entries {
			key := e.Source + "/" + e.RootID + "/" + e.Path
			if seen[key] {
				t.Fatal("duplicate cursor entry")
			}
			seen[key] = true
			if e.Source == "baseline" {
				baseline++
				if e.Outcome != domain.IgnoreBaselineUnknown || e.Family != "" || e.Reason != domain.IgnoreUnknownSource {
					t.Fatal("unknown lost or attributed to rules")
				}
			} else if e.Family == domain.IgnoreFamilyCustom {
				custom++
				if e.Reason != domain.IgnoreReasonRule || e.RuleLine != 1 {
					t.Fatal("custom provenance lost")
				}
			} else if e.Family == domain.IgnoreFamilyLegacy {
				legacy++
				if e.Reason != domain.IgnoreReasonBlank || e.RuleLine != 0 {
					t.Fatal("blank source provenance lost")
				}
			} else {
				t.Fatal("family missing")
			}
		}
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"digest", "identity", "sourceBytes", "proofVersion", rootPath} {
			if private != "" && strings.Contains(string(data), private) {
				t.Fatal("private evidence exposed")
			}
		}
		if firstCursor == "" {
			firstCursor = r.NextCursor
		}
		if r.NextCursor == "" {
			break
		}
		cursor = r.NextCursor
	}
	if baseline != 130 || custom != 1 || legacy != 2 || len(seen) != 133 {
		t.Fatal("incomplete family report", baseline, custom, legacy, len(seen))
	}
	if _, err := f.s.GetIgnoreReport(f.ctx, f.a, f.registration.Library.ID, 7, firstCursor); err != domain.ErrInvalid {
		t.Fatal("cross-job cursor admitted", err)
	}
	if _, err := f.s.GetIgnoreReport(f.ctx, domain.Actor{}, l.Job.ID, 7, ""); err == nil {
		t.Fatal("anonymous report exposed")
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=false WHERE id=$1::uuid`, f.a.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 7, firstCursor); err != domain.ErrForbidden {
		t.Fatal("demoted actor retained access", err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=true WHERE id=$1::uuid`, f.a.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, f.a.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 7, firstCursor); err != domain.ErrUnauthenticated {
		t.Fatal("revoked actor retained access", err)
	}
}

func TestFamilyReportLegacyInvalidated(t *testing.T) {
	f, l := familyComparisonFixture(t)
	var rootPath string
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT path FROM library_roots WHERE id=$1::uuid`, f.registration.RootID).Scan(&rootPath); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `UPDATE job_ignore_legacy_manifests SET invalidated=true WHERE job_id=$1::uuid`, l.Job.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.FinishJob(f.ctx, l, domain.JobFailed, "scan_unavailable"); err != nil {
		t.Fatal(err)
	}
	r, err := f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 100, "")
	if err != nil || !r.Invalidated || r.State != domain.JobFailed || len(r.Entries) != 3 {
		t.Fatal("legacy invalidation missing", err)
	}
}

func TestFamilyReportBaselineRuleProvenance(t *testing.T) {
	f, l := familyComparisonFixture(t)
	classifyFamilyForPublication(t, f, l, familyExcluded, true)
	if err := f.s.FinishFamilyIgnoreJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	r, err := f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 100, "")
	if err != nil || len(r.Entries) != 100 || r.Unknown != 0 || r.NextCursor == "" {
		t.Fatal("baseline report unavailable", err)
	}
	for _, e := range r.Entries {
		if e.Source != "baseline" || e.Outcome != domain.IgnoreBaselineExcluded || e.Family != domain.IgnoreFamilyLegacy || e.Reason != domain.IgnoreReasonBlank || e.RuleDirectory != "." || e.RuleLine != 0 || e.MatchedPath != e.Path {
			t.Fatal("baseline rule provenance lost")
		}
	}
}
