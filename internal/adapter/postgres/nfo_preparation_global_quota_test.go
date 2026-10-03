package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// These clones test SQL storage capacity only. Their synthetic scope/payload
// is never passed to a writer or treated as filesystem authorization.
const cloneQuotaPreparationActor = `INSERT INTO nfo_write_preparations SELECT (jsonb_populate_record(NULL::nfo_write_preparations,to_jsonb(p)||jsonb_build_object('id',gen_random_uuid(),'actor_id',$2::uuid,'idempotency_key',$3::text))).* FROM nfo_write_preparations p WHERE id=$1::uuid`

func requirePreparationQuotaRefusal(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40001" && (pgErr.Code != "23514" || pgErr.Message != "nfo write preparation capacity reached") {
		t.Fatal("global quota refused for unrelated reason", err)
	}
}

func TestNFOQuotaPreparationGlobalRowsConcurrentSnapshots(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			saved, _, err := service.Prepare(f.ctx, f.a, "global-row-base", request)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := f.s.Pool.Query(f.ctx, `INSERT INTO users(name,is_admin) SELECT 'preparation-row-actor-'||n,true FROM generate_series(1,17) n RETURNING id::text`)
			if err != nil {
				t.Fatal(err)
			}
			actors := []string{}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				actors = append(actors, id)
			}
			rows.Close()
			if rows.Err() != nil || len(actors) != 17 {
				t.Fatal("actor fixtures incomplete", rows.Err())
			}
			for i := 0; i < 254; i++ {
				if _, err := f.s.Pool.Exec(f.ctx, cloneQuotaPreparationActor, saved.ID, actors[i%16], fmt.Sprintf("global-row-%d", i)); err != nil {
					t.Fatal("seed below global row bound", err)
				}
			}
			first, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer first.Rollback(f.ctx)
			second, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer second.Rollback(f.ctx)
			for _, tx := range []pgx.Tx{first, second} {
				var count, actorMax int
				var size int64
				if err := tx.QueryRow(f.ctx, `SELECT count(*),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),0),(SELECT max(n) FROM (SELECT count(*) n FROM nfo_write_preparations GROUP BY actor_id) a) FROM nfo_write_preparations`).Scan(&count, &size, &actorMax); err != nil || count != 255 || actorMax >= 31 || size >= 134217728 {
					t.Fatal("row boundary masked by actor/library/byte bound", err, count, actorMax, size)
				}
			}
			if _, err := first.Exec(f.ctx, cloneQuotaPreparationActor, saved.ID, actors[0], "first-global-last-slot"); err != nil {
				t.Fatal(err)
			}
			if err := first.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			_, err = second.Exec(f.ctx, cloneQuotaPreparationActor, saved.ID, actors[16], "second-global-last-slot")
			if err == nil {
				err = second.Commit(f.ctx)
			}
			requirePreparationQuotaRefusal(t, err)
			_ = second.Rollback(f.ctx)
			var count, partial int
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*),count(*) FILTER(WHERE idempotency_key='second-global-last-slot') FROM nfo_write_preparations`).Scan(&count, &partial); err != nil || count != 256 || partial != 0 {
				t.Fatal("global row rejection retained excess data", err, count, partial)
			}
		})
	}
}

func quotaPreparationScope(t *testing.T, f jobFixture, base, key string) (string, string) {
	t.Helper()
	var library, item string
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO libraries SELECT (jsonb_populate_record(NULL::libraries,to_jsonb(l)||jsonb_build_object('id',gen_random_uuid(),'name',$2::text))).* FROM libraries l JOIN nfo_write_preparations p ON p.library_id=l.id WHERE p.id=$1::uuid RETURNING id::text`, base, key).Scan(&library); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(f.ctx, `INSERT INTO items SELECT (jsonb_populate_record(NULL::items,to_jsonb(i)||jsonb_build_object('id',gen_random_uuid(),'library_id',$2::uuid))).* FROM items i JOIN nfo_write_preparations p ON p.item_id=i.id WHERE p.id=$1::uuid RETURNING id::text`, base, library).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(f.ctx, `INSERT INTO library_roots SELECT (jsonb_populate_record(NULL::library_roots,to_jsonb(r)||jsonb_build_object('id',gen_random_uuid(),'library_id',$2::uuid,'path',r.path||'/quota-'||$2::text))).* FROM library_roots r JOIN nfo_write_preparations p ON p.root_id=r.id WHERE p.id=$1::uuid`, base, library); err != nil {
		t.Fatal("create owned quota root", err)
	}
	return library, item
}

const insertQuotaPreparationBytes = `WITH large AS MATERIALIZED (SELECT convert_to(rpad('<movie/>',$5::integer,' '),'UTF8') AS payload), recipe AS MATERIALIZED (SELECT p.*,convert_to((convert_from(request_bytes,'UTF8')::jsonb||jsonb_build_object('itemId',$3::uuid,'maxBytes',$5::bigint))::text,'UTF8') AS encoded FROM nfo_write_preparations p WHERE id=$1::uuid)
 INSERT INTO nfo_write_preparations(actor_id,idempotency_key,version,request_bytes,request_digest,library_id,item_id,source_id,root_id,kind,revision,generation,root_generation,root_path,relative_path,media_path,directory_path,max_bytes,modified_unix_nano,original_bytes,original_sha256,replacement_bytes,replacement_sha256,created_at,expires_at)
 SELECT actor_id,$4,version,encoded,encode(sha256(encoded),'hex'),$2::uuid,$3::uuid,source_id,(SELECT id FROM library_roots WHERE library_id=$2::uuid),kind,revision,generation,(SELECT nfo_generation FROM library_roots WHERE library_id=$2::uuid),(SELECT path FROM library_roots WHERE library_id=$2::uuid),relative_path,media_path,directory_path,$5::bigint,modified_unix_nano,payload,encode(sha256(payload),'hex'),payload,encode(sha256(payload),'hex'),created_at,expires_at FROM recipe CROSS JOIN large`

func TestNFOQuotaPreparationGlobalBytesConcurrentSnapshots(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			f, service, _, request := nfoWritePreparationFixture(t)
			saved, _, err := service.Prepare(f.ctx, f.a, "global-byte-base", request)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				library, item := quotaPreparationScope(t, f, saved.ID, fmt.Sprintf("large-quota-library-%d", i))
				if _, err := f.s.Pool.Exec(f.ctx, insertQuotaPreparationBytes, saved.ID, library, item, fmt.Sprintf("large-seed-%d", i), 33554432); err != nil {
					t.Fatal("seed below global bytes bound", err)
				}
			}
			firstLibrary, firstItem := quotaPreparationScope(t, f, saved.ID, "first-quota-library")
			secondLibrary, secondItem := quotaPreparationScope(t, f, saved.ID, "second-quota-library")
			first, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer first.Rollback(f.ctx)
			second, err := f.s.Pool.BeginTx(f.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer second.Rollback(f.ctx)
			for _, tx := range []pgx.Tx{first, second} {
				var count int
				var size, libraryMax int64
				if err := tx.QueryRow(f.ctx, `SELECT count(*),sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),(SELECT max(n) FROM (SELECT sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)) n FROM nfo_write_preparations GROUP BY library_id) a) FROM nfo_write_preparations`).Scan(&count, &size, &libraryMax); err != nil || count != 4 || size <= 3*67108864 || size >= 268435456-33554432 || libraryMax >= 134217728 {
					t.Fatal("byte boundary masked by actor/library/row bound", err, count, size, libraryMax)
				}
			}
			if _, err := first.Exec(f.ctx, insertQuotaPreparationBytes, saved.ID, firstLibrary, firstItem, "first-global-last-bytes", 16777216); err != nil {
				t.Fatal(err)
			}
			if err := first.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			_, err = second.Exec(f.ctx, insertQuotaPreparationBytes, saved.ID, secondLibrary, secondItem, "second-global-last-bytes", 16777216)
			if err == nil {
				err = second.Commit(f.ctx)
			}
			requirePreparationQuotaRefusal(t, err)
			_ = second.Rollback(f.ctx)
			var count, partial int
			var size int64
			if err := f.s.Pool.QueryRow(f.ctx, `SELECT count(*),sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),count(*) FILTER(WHERE idempotency_key='second-global-last-bytes') FROM nfo_write_preparations`).Scan(&count, &size, &partial); err != nil || count != 5 || partial != 0 || size <= 7*33554432 || size >= 268435456 {
				t.Fatal("global byte rejection retained excess data", err, count, size, partial)
			}
		})
	}
}
