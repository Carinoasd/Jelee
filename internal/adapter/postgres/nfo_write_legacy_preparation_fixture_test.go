package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/nfo"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"github.com/jackc/pgx/v5"
)

// Build real controlled bytes before selecting a historical schema, then insert
// its original SQL shape into the owned fixture. Current production write ports
// require schema52 root evidence and must not acquire a legacy fallback.
func legacyNFOWritePreparationFixture(t *testing.T, f jobFixture, request domain.NFOWritePrepareRequest, key string, version uint) domain.NFOWritePreparation {
	t.Helper()
	scope, err := f.s.ResolveItemNFO(f.ctx, f.a, request.ItemID, request.Revision)
	if err != nil {
		t.Fatal(err)
	}
	budget, _ := resources.New(resources.Limits{CPU: 1, IO: 1, Total: 1, Queue: 0})
	preparer, _ := nfo.NewWritePreparer(budget)
	p, err := preparer.PrepareNFOWrite(f.ctx, scope, request)
	if err != nil {
		t.Fatal(err)
	}
	jobMetricMigration(t, f, "down", version)
	raw, _ := json.Marshal(p.Request)
	digest, _ := domain.NFOWriteRequestDigest(p.Request)
	hash := sha256.Sum256(p.Replacement)
	err = f.s.Pool.QueryRow(f.ctx, `INSERT INTO nfo_write_preparations(actor_id,idempotency_key,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256) VALUES($1::uuid,$2,$3,$4,$5,$6::uuid,$7::uuid,$8::uuid,$9::uuid,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22) RETURNING id::text`, f.a.UserID, key, p.Version, raw, digest, scope.LibraryID, scope.ItemID, scope.SourceID, scope.RootID, scope.Kind, scope.Revision, scope.Generation, scope.Source.RootPath, scope.Source.RelativePath, scope.MediaPath, scope.DirectoryPath, p.Request.MaxBytes, p.Stamp.ModifiedUnixNano, p.Original, p.Stamp.SHA256, p.Replacement, hex.EncodeToString(hash[:])).Scan(&p.ID)
	if err != nil {
		t.Fatal("insert historical preparation", err)
	}
	return p
}

func copyLegacyNFOWriteFixture(t *testing.T, f jobFixture, tx pgx.Tx, job domain.Job, p domain.NFOWritePreparation) error {
	t.Helper()
	raw, _ := json.Marshal(struct {
		Priority     string
		Preparations []string
	}{job.Priority, []string{p.ID}})
	digest := sha256.Sum256(raw)
	if _, err := tx.Exec(f.ctx, `INSERT INTO nfo_write_requests(job_id,library_id,generation,total,intent_digest) VALUES($1::uuid,$2::uuid,$3,1,$4)`, job.ID, p.Scope.LibraryID, p.Scope.Generation, digest[:]); err != nil {
		return err
	}
	_, err := tx.Exec(f.ctx, `INSERT INTO nfo_write_entries(job_id,sequence,preparation_id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256) SELECT $1::uuid,1,id,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256 FROM nfo_write_preparations WHERE id=$2::uuid`, job.ID, p.ID)
	return err
}
