//go:build jelee_probe_tests

package runtime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/postgres"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type imagesSoakRotationRole struct {
	Rotated     bool `json:"rotated"`
	OldRejected bool `json:"oldRejected"`
	NewAccepted bool `json:"newAccepted"`
}

type imagesSoakRotation struct {
	AtRound int                    `json:"atRound"`
	Admin   imagesSoakRotationRole `json:"admin"`
	Viewer  imagesSoakRotationRole `json:"viewer"`
}

// Returns the private new grant separately from serializable boolean evidence.
// The real /auth/rotate route must revoke the old token and preserve role/TTL.
func rotateImagesSoakSession(ctx context.Context, client *http.Client, address string, old domain.SessionGrant) (domain.SessionGrant, imagesSoakRotationRole, error) {
	var grant domain.SessionGrant
	var evidence imagesSoakRotationRole
	if imagesMemoryAPI(ctx, client, address, "POST", "/api/v1/auth/rotate", []byte(`{"deviceName":"soak"}`), old.Token, "", 200, &grant) != nil ||
		grant.Token == "" || grant.Token == old.Token || !domain.ValidID(grant.Session.ID) || grant.Session.ID == old.Session.ID ||
		grant.User.ID != old.User.ID || !domain.ValidID(grant.User.ID) || grant.User.Admin != old.User.Admin ||
		grant.Session.UserID != old.User.ID || grant.Session.ClientKind != old.Session.ClientKind ||
		grant.Session.DeviceName != "soak" || grant.Session.RevokedAt != nil ||
		grant.Session.ExpiresAt.Sub(grant.Session.CreatedAt) != 24*time.Hour {
		return domain.SessionGrant{}, evidence, errImagesMemoryHTTP
	}
	evidence.Rotated = true
	request, err := http.NewRequestWithContext(ctx, "GET", address+"/api/v1/users/me", nil)
	if err != nil {
		return domain.SessionGrant{}, evidence, errImagesMemoryHTTP
	}
	request.Header.Set("Authorization", "Bearer "+old.Token)
	response, err := client.Do(request)
	if err != nil {
		return domain.SessionGrant{}, evidence, errImagesMemoryHTTP
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || len(data) > 4096 || response.StatusCode != 401 {
		return domain.SessionGrant{}, evidence, errImagesMemoryHTTP
	}
	evidence.OldRejected = true
	var user domain.User
	if imagesMemoryAPI(ctx, client, address, "GET", "/api/v1/users/me", nil, grant.Token, "", 200, &user) != nil || user.ID != old.User.ID || user.Admin != old.User.Admin {
		return domain.SessionGrant{}, evidence, errImagesMemoryHTTP
	}
	evidence.NewAccepted = true
	return grant, evidence, nil
}

type imagesSoakScan struct {
	Files           int64 `json:"files"`
	Video           int64 `json:"video"`
	Image           int64 `json:"image"`
	Bytes           int64 `json:"bytes"`
	DoneDirectories int64 `json:"doneDirectories"`
	Pending         int64 `json:"pending"`
	Skipped         int64 `json:"skipped"`
	Missing         int64 `json:"missing"`
	Published       bool  `json:"published"`
}

type imagesSoakResources struct {
	RuntimeConnections  int64 `json:"runtimeConnections"`
	ObserverConnections int64 `json:"observerConnections"`
	ActiveJobs          int64 `json:"activeJobs"`
	ActiveLeases        int64 `json:"activeLeases"`
	TerminalJobs        int64 `json:"terminalJobs"`
	ActiveSessions      int64 `json:"activeSessions"`
}

type imagesSoakCheckpoint struct {
	Round        int                 `json:"round"`
	ElapsedNanos int64               `json:"elapsedNanos"`
	Resident     residentSample      `json:"resident"`
	Processor    imagesMemoryStats   `json:"processor"`
	Resources    imagesSoakResources `json:"resources"`
}

type imagesSoakRound struct {
	Index          int                     `json:"index"`
	ScheduledNanos int64                   `json:"scheduledNanos"`
	StartedNanos   int64                   `json:"startedNanos"`
	FinishedNanos  int64                   `json:"finishedNanos"`
	Scan           imagesSoakScan          `json:"scan"`
	Cold           imagesMemoryPhaseResult `json:"cold"`
	Warm           imagesMemoryPhaseResult `json:"warm"`
	Checkpoint     imagesSoakCheckpoint    `json:"checkpoint"`
}

func readImagesSoakResources(ctx context.Context, store *postgres.Store, applicationName, observerName string) (imagesSoakResources, error) {
	var result imagesSoakResources
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := store.Pool.QueryRow(c, `SELECT
	(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1),
	(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=$2),
	(SELECT count(*) FROM jobs WHERE state IN ('queued','running')),
	(SELECT count(*) FROM jobs WHERE owner IS NOT NULL OR lease_until IS NOT NULL),
	(SELECT count(*) FROM jobs WHERE state IN ('succeeded','failed','cancelled')),
	(SELECT count(*) FROM sessions WHERE revoked_at IS NULL AND expires_at>now())`, applicationName, observerName).Scan(
		&result.RuntimeConnections, &result.ObserverConnections, &result.ActiveJobs, &result.ActiveLeases, &result.TerminalJobs, &result.ActiveSessions)
	if err != nil || !imagesSoakResourcesValid(result) {
		return result, errImagesMemoryHTTP
	}
	return result, nil
}

func imagesSoakResourcesValid(value imagesSoakResources) bool {
	// History is trimmed on submission, so a completed new job may be the 21st.
	return value.RuntimeConnections >= 0 && value.RuntimeConnections <= 8 &&
		value.ObserverConnections >= 1 && value.ObserverConnections <= 4 &&
		value.ActiveJobs == 0 && value.ActiveLeases == 0 &&
		value.TerminalJobs >= 0 && value.TerminalJobs <= 21 && value.ActiveSessions == 2
}

func runImagesSoakScan(ctx context.Context, client *http.Client, address, token string, store *postgres.Store, registration domain.LibraryRegistration, round int, fixtureBytes int64) (imagesSoakScan, string) {
	var result imagesSoakScan
	if round < 0 || round >= 288 || fixtureBytes <= 0 {
		return result, "soak_scan_input_invalid"
	}
	var job domain.Job
	if imagesMemoryAPI(ctx, client, address, "POST", "/api/v1/libraries/"+registration.Library.ID+"/scan",
		[]byte(`{"priority":"manual","probe":false,"nfo":false}`), token, fmt.Sprintf("soak-scan-%03d", round), 202, &job) != nil || !domain.ValidID(job.ID) {
		return result, "soak_scan_admission_failed"
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if imagesMemoryAPI(ctx, client, address, "GET", "/api/v1/jobs/"+job.ID, nil, token, "", 200, &job) != nil {
			return result, "soak_scan_status_failed"
		}
		if job.State == domain.JobSucceeded || job.State == domain.JobFailed || job.State == domain.JobCancelled {
			break
		}
		select {
		case <-ctx.Done():
			return result, "soak_scan_timeout"
		case <-ticker.C:
		}
	}
	if job.State != domain.JobSucceeded || job.Attempts != 1 || job.Files != 2008 || job.Bytes != fixtureBytes || job.Skipped != 0 || job.Missing != 0 || job.ReviewRequired || job.ErrorCode != "" {
		return result, "soak_scan_result_mismatch"
	}
	queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Mixed images/video and variable file sizes require independent aggregates;
	// the 500k video's fixed 34-byte fixture verifier is intentionally untouched.
	var invalid, files, size, videos, images int64
	queries := []struct {
		sql string
		id  string
	}{
		{`SELECT count(*),COALESCE(sum(size),0),count(*) FILTER(WHERE kind='video'),count(*) FILTER(WHERE kind='image'),count(*) FILTER(WHERE root_id<>$2::uuid OR kind NOT IN ('video','image')) FROM job_inventory WHERE job_id=$1::uuid`, job.ID},
		{`SELECT count(*),COALESCE(sum(size),0),count(*) FILTER(WHERE kind='video'),count(*) FILTER(WHERE kind='image'),count(*) FILTER(WHERE root_id<>$2::uuid OR NOT attributes_known OR kind NOT IN ('video','image')) FROM library_inventory_baseline WHERE library_id=$1::uuid`, registration.Library.ID},
	}
	for _, query := range queries {
		if err := store.Pool.QueryRow(queryCtx, query.sql, query.id, registration.RootID).Scan(&files, &size, &videos, &images, &invalid); err != nil || files != 2008 || videos != 1004 || images != 1004 || size != fixtureBytes || invalid != 0 {
			return result, "soak_scan_inventory_mismatch"
		}
	}
	result.Files, result.Video, result.Image, result.Bytes = files, videos, images, size
	var roots int64
	if err := store.Pool.QueryRow(queryCtx, `SELECT count(*) FROM library_roots WHERE library_id=$1::uuid`, registration.Library.ID).Scan(&roots); err != nil || roots != 1 {
		return result, "soak_scan_roots_mismatch"
	}
	if err := store.Pool.QueryRow(queryCtx, `SELECT count(*) FILTER(WHERE done),count(*) FILTER(WHERE NOT done),count(*) FILTER(WHERE root_id<>$2::uuid) FROM job_directories WHERE job_id=$1::uuid`, job.ID, registration.RootID).Scan(&result.DoneDirectories, &result.Pending, &invalid); err != nil || result.DoneDirectories != 106 || result.Pending != 0 || invalid != 0 {
		return result, "soak_scan_frontier_mismatch"
	}
	var ignore bool
	var nfo string
	var optional int64
	if err := store.Pool.QueryRow(queryCtx, `SELECT EXISTS(SELECT 1 FROM inventory_snapshot_preparations p JOIN libraries l ON l.id=p.library_id WHERE p.job_id=$1::uuid AND p.ready AND p.cleaned AND p.copied=2008 AND p.source_files=2008 AND l.active_inventory_snapshot=p.snapshot_id),j.ignore_requested,l.nfo_mode,
	(SELECT count(*) FROM probe_requests WHERE job_id=$1::uuid)+(SELECT count(*) FROM probe_job_state WHERE job_id=$1::uuid)+(SELECT count(*) FROM nfo_job_requests WHERE job_id=$1::uuid AND (requested OR mode<>'off'))+(SELECT count(*) FROM nfo_job_state WHERE job_id=$1::uuid AND mode<>'off')+(SELECT count(*) FROM job_ignore_requests WHERE job_id=$1::uuid)
	FROM jobs j JOIN libraries l ON l.id=j.library_id WHERE j.id=$1::uuid`, job.ID).Scan(&result.Published, &ignore, &nfo, &optional); err != nil || !result.Published || ignore || nfo != domain.NFOModeOff || optional != 0 {
		return result, "soak_scan_snapshot_mismatch"
	}
	return result, ""
}
