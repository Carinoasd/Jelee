package postgres

import (
	"encoding/json"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"strings"
	"testing"
)

func TestIgnoreReportTerminalPaginationAndAuthorization(t *testing.T) {
	f, l, d, b := ignoreScanFixture(t)
	b.Inventory.Done = true
	if err := f.s.SaveIgnoreScanBatch(f.ctx, l, d, b); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 1, ""); err != domain.ErrConflict {
		t.Fatal("mutable report exposed", err)
	}
	classifyForPublication(t, f, l, func(*domain.IgnoreBaselineDecision) {}, true)
	if err := f.s.FinishIgnoreJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	first, err := f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 1, "")
	if err != nil || !first.Enabled || first.ExcludedFiles != 1 || first.ExcludedDirectories != 1 || len(first.Entries) != 1 || first.NextCursor == "" {
		t.Fatal("first report page", err)
	}
	second, err := f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 1, first.NextCursor)
	if err != nil || len(second.Entries) != 1 || second.NextCursor != "" || second.Entries[0].Path == first.Entries[0].Path {
		t.Fatal("second report page", err)
	}
	for _, p := range []domain.IgnoreReport{first, second} {
		e := p.Entries[0]
		if e.Source != "scan" || e.RuleDirectory != "." || e.RuleLine != 1 || e.MatchedPath != e.Path || e.Outcome != domain.IgnoreBaselineExcluded {
			t.Fatal("lost provenance")
		}
		data, _ := json.Marshal(p)
		if strings.Contains(string(data), d.RootPath) {
			t.Fatal("absolute path exposed")
		}
	}
	if _, err = f.s.GetIgnoreReport(f.ctx, f.a, f.registration.Library.ID, 1, first.NextCursor); err != domain.ErrInvalid {
		t.Fatal("cross-job cursor accepted", err)
	}
	if _, err = f.s.GetIgnoreReport(f.ctx, domain.Actor{}, l.Job.ID, 1, ""); err == nil {
		t.Fatal("unauthorized report exposed")
	}
	if _, err = f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 101, ""); err != domain.ErrInvalid {
		t.Fatal("unbounded page accepted", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=false WHERE id=$1::uuid`, f.a.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 1, first.NextCursor); err != domain.ErrForbidden {
		t.Fatal("demoted actor retained report access", err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE users SET is_admin=true WHERE id=$1::uuid`, f.a.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, f.a.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 1, first.NextCursor); err != domain.ErrUnauthenticated {
		t.Fatal("revoked session retained report access", err)
	}
}

func TestIgnoreReportRetainsUnknownReason(t *testing.T) {
	f, l, _ := baselineComparisonFixture(t, 2, 0)
	classifyForPublication(t, f, l, func(d *domain.IgnoreBaselineDecision) {
		*d = domain.IgnoreBaselineDecision{RootID: d.RootID, Path: d.Path, Outcome: domain.IgnoreBaselineUnknown, Reason: domain.IgnoreUnknownSource}
	}, false)
	if err := f.s.FinishIgnoreJob(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	report, err := f.s.GetIgnoreReport(f.ctx, f.a, l.Job.ID, 100, "")
	if err != nil || report.Unknown != 2 || !report.ReviewRequired || len(report.Entries) != 2 {
		t.Fatal("unknown review lost", err)
	}
	for _, e := range report.Entries {
		if e.Source != "baseline" || e.Outcome != domain.IgnoreBaselineUnknown || e.Reason != domain.IgnoreUnknownSource {
			t.Fatal("unknown became absence")
		}
	}
}
