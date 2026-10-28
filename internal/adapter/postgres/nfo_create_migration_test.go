package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestNFOCreateMigrationRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	jobMetricMigration(t, f, "down", downgradeAboveMigration(t, f, "nfo_create_target")-1)
	var removed bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT to_regprocedure('nfo_absent_target_key(bytea,text)') IS NULL AND to_regprocedure('check_nfo_create_claim_scope(text,uuid,integer)') IS NULL AND to_regprocedure('claim_nfo_create_output()') IS NULL AND to_regprocedure('check_nfo_commit_catalog_v87(text,uuid,integer)') IS NULL
	 AND NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND column_name='create_target')`).Scan(&removed); err != nil || !removed {
		t.Fatal("empty downgrade left create objects", err)
	}
	// The older schema is strict again: no empty original, no absent claim.
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_native_claims(kind,identity,token) VALUES('nfo_absent',decode(repeat('ff',48),'hex'),gen_random_uuid())`); err == nil {
		t.Fatal("older schema accepts an absent-target claim")
	}
	jobMetricMigration(t, f, "up", SchemaVersion)
	if err := f.s.Ready(f.ctx); err != nil {
		t.Fatal("round-trip schema is not ready", err)
	}
	// A create works on the schema that was migrated back up.
	service, _, request := nfoCreateService(t, f)
	prepared, _, err := service.Prepare(f.ctx, f.a, "after-round-trip", request)
	if err != nil || !prepared.Request.Create {
		t.Fatal("create preparation failed after the round trip", err)
	}
}

func TestNFOCreateMigrationRefusesRetainedData(t *testing.T) {
	for _, stage := range []string{"preparation", "claims"} {
		t.Run(stage, func(t *testing.T) {
			f := newJobFixture(t)
			if stage == "preparation" {
				service, _, request := nfoCreateService(t, f)
				if _, _, err := service.Prepare(f.ctx, f.a, "retained", request); err != nil {
					t.Fatal(err)
				}
			} else {
				s := newNFOCreateJob(t)
				f = s.f
				l := s.claim(t, nfoWriteTestOwner)
				if _, err := f.s.BeginNFOWriteCommit(f.ctx, l, 1); err != nil {
					t.Fatal(err)
				}
			}
			want := downgradeAboveMigration(t, f, "nfo_create_target")
			nfoMigrationDenied(t, f, "000090_nfo_create_target.down.sql")
			if _, _, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "down"); err == nil {
				t.Fatal("retained create data downgraded")
			}
			version, dirty, err := Migrate(f.ctx, f.s.Pool.Config().ConnString(), "status")
			if err != nil || version != want-1 || !dirty {
				t.Fatal("refused downgrade lost its dirty status", version, dirty, err)
			}
			var kept int
			if err = f.s.Pool.QueryRow(f.ctx, `SELECT count(*) FROM nfo_write_preparations WHERE create_target`).Scan(&kept); err != nil || kept != 1 {
				t.Fatal("refused downgrade removed the create preparation", kept, err)
			}
		})
	}
}

// pgCode reports the SQLSTATE of a database error.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// copyPreparation inserts a copy of a stored preparation with some columns
// replaced, to show which combinations the schema accepts.
func copyPreparation(t *testing.T, f jobFixture, id, key string, overrides map[string]string) error {
	t.Helper()
	columns := []string{"actor_id", "idempotency_key", "version", "request_bytes", "request_digest", "library_id", "item_id", "source_id", "root_id", "kind", "revision", "generation", "root_generation", "root_path", "relative_path", "media_path", "directory_path", "max_bytes", "modified_unix_nano", "original_bytes", "original_sha256", "replacement_bytes", "replacement_sha256", "native_receipt", "create_target"}
	selects := make([]string, len(columns))
	for i, column := range columns {
		selects[i] = column
		if column == "idempotency_key" {
			selects[i] = "'" + key + "'"
		}
		if override, ok := overrides[column]; ok {
			selects[i] = override
		}
	}
	_, err := f.s.Pool.Exec(f.ctx, `INSERT INTO nfo_write_preparations(`+strings.Join(columns, ",")+`) SELECT `+strings.Join(selects, ",")+` FROM nfo_write_preparations WHERE id=$1::uuid`, id)
	return err
}

func TestNFOCreateSchemaKeepsRewriteRulesStrict(t *testing.T) {
	f := newJobFixture(t)
	service, scope, request := nfoCreateService(t, f)
	create, _, err := service.Prepare(f.ctx, f.a, "strict-create", request)
	if err != nil {
		t.Fatal(err)
	}
	empty := sha256.Sum256(nil)
	emptyHash := "'" + hex.EncodeToString(empty[:]) + "'"
	// The copy of a create preparation is accepted unchanged (control).
	if err := copyPreparation(t, f, create.ID, "control", nil); err != nil {
		t.Fatal("control copy of a create preparation was refused", err)
	}
	// A create preparation needs its flag, and only it may have no original.
	for name, overrides := range map[string]map[string]string{
		"flag off":                 {"create_target": "false"},
		"with original":            {"original_bytes": "'x'::bytea", "original_sha256": "encode(sha256('x'::bytea),'hex')"},
		"flag off and an original": {"create_target": "false", "original_bytes": "'x'::bytea", "original_sha256": "encode(sha256('x'::bytea),'hex')"},
	} {
		if err := copyPreparation(t, f, create.ID, "create-"+strings.ReplaceAll(name, " ", "-"), overrides); pgCode(err) != "23514" {
			t.Fatalf("%s: a create preparation with a rewrite shape was accepted: %v", name, err)
		}
	}
	// Rewrite rows stay as strict as before: a real original, no create flag.
	if err := os.WriteFile(filepath.Join(scope.Source.RootPath, filepath.FromSlash(scope.Source.RelativePath)), []byte("<movie><title>old</title></movie>"), 0o600); err != nil {
		t.Fatal(err)
	}
	rewrite := request
	rewrite.Create, rewrite.Backups, rewrite.CreateMissing = false, 2, true
	prepared, _, err := service.Prepare(f.ctx, f.a, "strict-rewrite", rewrite)
	if err != nil || prepared.Request.Create || len(prepared.Original) == 0 {
		t.Fatal("rewrite preparation unavailable", err)
	}
	for name, overrides := range map[string]map[string]string{
		"empty original":    {"original_bytes": "''::bytea", "original_sha256": emptyHash},
		"create flag":       {"create_target": "true"},
		"flag and no bytes": {"create_target": "true", "original_bytes": "''::bytea", "original_sha256": emptyHash},
	} {
		if err := copyPreparation(t, f, prepared.ID, "rewrite-"+strings.ReplaceAll(name, " ", "-"), overrides); pgCode(err) != "23514" {
			t.Fatalf("%s: a rewrite preparation with a create shape was accepted: %v", name, err)
		}
	}
	if err := copyPreparation(t, f, prepared.ID, "rewrite-control", nil); err != nil {
		t.Fatal("control copy of a rewrite preparation was refused", err)
	}
}

// fakeFileIdentity is a canonical file identity of another object: the first
// observation's media record with a different inode.
func fakeFileIdentity(base [48]byte, n byte) []byte {
	out := append([]byte{}, base[:]...)
	out[16], out[17] = n, 0xa5
	return out
}

// TestNFOCreateEvidenceContract drives the evidence tables directly: the
// schema accepts a create only in its own shape and never a rewrite shape for
// it, and a rewrite never takes a create shape.
func TestNFOCreateEvidenceContract(t *testing.T) {
	s := newNFOCreateJob(t)
	p := s.prepared[0]
	l := s.claim(t, nfoWriteTestOwner)
	record, err := s.f.s.BeginNFOWriteCommit(s.f.ctx, l, 1)
	if err != nil {
		t.Fatal(err)
	}
	ancestors := p.NativeObservation.AncestorIdentities()
	parent := ancestors[len(ancestors)-1]
	media := p.NativeObservation.MediaIdentity()
	exec := func(query string, args ...any) error {
		_, err := s.f.s.Pool.Exec(s.f.ctx, query, args...)
		return err
	}
	insertPlan := func(version int, parentIdentity [48]byte, target any) error {
		return exec(`INSERT INTO nfo_write_commit_file_plans(token,version,target_name,parent_identity,target_identity) VALUES($1::uuid,$2,'film.nfo',$3,$4)`, record.Token, version, parentIdentity[:], target)
	}
	deny := func(what string, err error) {
		t.Helper()
		if pgCode(err) != "23514" {
			t.Fatalf("%s: the database did not refuse it with a check failure: %v", what, err)
		}
	}
	// A create plan has version 2, the observed parent and no target identity.
	deny("version 1 with a target for a create entry", insertPlan(1, parent, media[:]))
	deny("version 1 without a target", insertPlan(1, parent, nil))
	deny("version 2 with a target", insertPlan(2, parent, media[:]))
	var elsewhere [48]byte
	copy(elsewhere[:], fakeFileIdentity(parent, 7))
	elsewhere[2] = 2
	deny("version 2 under another directory", insertPlan(2, elsewhere, nil))
	if err := insertPlan(2, parent, nil); err != nil {
		t.Fatal("the create plan was refused", err)
	}
	// Evidence of a create: one checkpoint phase, an output, no rollback.
	output := fakeFileIdentity(media, 1)
	checkpoint := func(phase int, out []byte, rollback any) error {
		return exec(`INSERT INTO nfo_write_commit_file_checkpoints(token,phase,output_identity,rollback_identity) VALUES($1::uuid,$2,$3,$4)`, record.Token, phase, out, rollback)
	}
	ready := func(out []byte, rollback any) error {
		return exec(`INSERT INTO nfo_write_commit_files_ready(token,output_identity,rollback_identity) VALUES($1::uuid,$2,$3)`, record.Token, out, rollback)
	}
	deny("ready without a checkpoint", ready(output, nil))
	deny("a second checkpoint phase with a rollback object", checkpoint(2, output, fakeFileIdentity(media, 2)))
	if err := checkpoint(1, output, nil); err != nil {
		t.Fatal("the create checkpoint was refused", err)
	}
	deny("ready with a rollback object", ready(output, fakeFileIdentity(media, 2)))
	deny("ready for another output", ready(fakeFileIdentity(media, 3), nil))
	if err := ready(output, nil); err != nil {
		t.Fatal("the create ready was refused", err)
	}
	var claimed []byte
	var claims int
	if err := s.f.s.Pool.QueryRow(s.f.ctx, `SELECT count(*),(array_agg(identity) FILTER (WHERE kind='nfo'))[1] FROM nfo_write_native_claims WHERE token=$1::uuid`, record.Token).Scan(&claims, &claimed); err != nil || claims != 3 || !bytes.Equal(claimed, output) {
		t.Fatal("ready did not claim its output identity", claims, err)
	}
	// Claims cannot be forged for a create token.
	deny("a second output claim", exec(`INSERT INTO nfo_write_native_claims(kind,identity,token) VALUES('nfo',$1,$2::uuid)`, fakeFileIdentity(media, 4), record.Token))
	var count int
	if err := s.f.s.Pool.QueryRow(s.f.ctx, `SELECT count(*) FROM nfo_write_native_claims WHERE token=$1::uuid`, record.Token).Scan(&count); err != nil || count != 3 {
		t.Fatal("a forged claim was stored", count, err)
	}
}

// The key of the absent-target claim is derived inside the database from the
// first observation; it cannot be mistaken for a physical identity.
func TestNFOCreateAbsentTargetKey(t *testing.T) {
	f := newJobFixture(t)
	var distinct bool
	var first byte
	var valid bool
	if err := f.s.Pool.QueryRow(f.ctx, `SELECT
	 nfo_absent_target_key(decode(repeat('01',48),'hex'),'film.nfo')<>nfo_absent_target_key(decode(repeat('02',48),'hex'),'film.nfo')
	 AND nfo_absent_target_key(decode(repeat('01',48),'hex'),'film.nfo')<>nfo_absent_target_key(decode(repeat('01',48),'hex'),'Film.nfo')
	 AND nfo_absent_target_key(decode(repeat('01',48),'hex'),'film.nfo')<>set_byte(sha384(decode(repeat('01',48),'hex')||convert_to('film.nfo','UTF8')),0,255)
	 AND octet_length(nfo_absent_target_key(decode(repeat('01',48),'hex'),'film.nfo'))=48,
	 get_byte(nfo_absent_target_key(decode(repeat('01',48),'hex'),'film.nfo'),0),
	 nfo_commit_identity_valid(nfo_absent_target_key(decode(repeat('01',48),'hex'),'film.nfo'),1) OR nfo_commit_identity_valid(nfo_absent_target_key(decode(repeat('01',48),'hex'),'film.nfo'),2)`).Scan(&distinct, &first, &valid); err != nil || !distinct || first != 255 || valid {
		t.Fatal("the absent-target key is not a distinct, domain-separated, non-identity value", distinct, first, valid, err)
	}
	// A claim table row of one kind cannot carry the other kind's key space.
	deny := func(err error) {
		t.Helper()
		if pgCode(err) != "23514" {
			t.Fatalf("expected a check failure: %v", err)
		}
	}
	deny(f.s.Pool.QueryRow(f.ctx, `INSERT INTO nfo_write_native_claims(kind,identity,token) VALUES('nfo',decode(repeat('ff',48),'hex'),gen_random_uuid())`).Scan())
	deny(f.s.Pool.QueryRow(f.ctx, `INSERT INTO nfo_write_native_claims(kind,identity,token) VALUES('nfo_absent',decode(repeat('01',48),'hex'),gen_random_uuid())`).Scan())
}

// A rewrite journal can hold neither an absent-target claim nor a plan of
// version 2.
func TestNFOCreateShapesAreRefusedForARewrite(t *testing.T) {
	s := newNFOWriteJob(t, 1)
	p := s.prepared[0]
	l := s.claim(t, nfoWriteTestOwner)
	record, err := s.f.s.BeginNFOWriteCommit(s.f.ctx, l, 1)
	if err != nil {
		t.Fatal(err)
	}
	ancestors := p.NativeObservation.AncestorIdentities()
	parent := ancestors[len(ancestors)-1]
	target := p.NativeObservation.NFOFileIdentity()
	exec := func(query string, args ...any) error {
		_, err := s.f.s.Pool.Exec(s.f.ctx, query, args...)
		return err
	}
	var key []byte
	if err := s.f.s.Pool.QueryRow(s.f.ctx, `SELECT nfo_absent_target_key($1,'film.nfo')`, parent[:]).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if pgCode(exec(`INSERT INTO nfo_write_native_claims(kind,identity,token) VALUES('nfo_absent',$1,$2::uuid)`, key, record.Token)) != "23514" {
		t.Fatal("a rewrite token took an absent-target claim")
	}
	if pgCode(exec(`INSERT INTO nfo_write_commit_file_plans(token,version,target_name,parent_identity,target_identity) VALUES($1::uuid,2,'film.nfo',$2,NULL)`, record.Token, parent[:])) != "23514" {
		t.Fatal("a rewrite entry accepted a create plan")
	}
	if pgCode(exec(`INSERT INTO nfo_write_commit_file_plans(token,version,target_name,parent_identity,target_identity) VALUES($1::uuid,1,'film.nfo',$2,NULL)`, record.Token, parent[:])) != "23514" {
		t.Fatal("a rewrite plan without a target identity was accepted")
	}
	if err := exec(`INSERT INTO nfo_write_commit_file_plans(token,version,target_name,parent_identity,target_identity) VALUES($1::uuid,1,'film.nfo',$2,$3)`, record.Token, parent[:], target[:]); err != nil {
		t.Fatal("the rewrite plan was refused", err)
	}
	// A rewrite ready needs its rollback object.
	output, rollback := fakeFileIdentity(target, 1), fakeFileIdentity(target, 2)
	if pgCode(exec(`INSERT INTO nfo_write_commit_file_checkpoints(token,phase,output_identity,rollback_identity) VALUES($1::uuid,1,$2,NULL)`, record.Token, output)) != "" {
		t.Fatal("the rewrite checkpoint was refused")
	}
	if pgCode(exec(`INSERT INTO nfo_write_commit_file_checkpoints(token,phase,output_identity,rollback_identity) VALUES($1::uuid,2,$2,$3)`, record.Token, output, rollback)) != "" {
		t.Fatal("the rewrite second checkpoint was refused")
	}
	if pgCode(exec(`INSERT INTO nfo_write_commit_files_ready(token,output_identity,rollback_identity) VALUES($1::uuid,$2,NULL)`, record.Token, output)) != "23514" {
		t.Fatal("a rewrite ready without a rollback object was accepted")
	}
}
