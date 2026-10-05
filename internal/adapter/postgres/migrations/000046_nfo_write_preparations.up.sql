BEGIN;
-- Prepared output is private immutable data, not permission to touch a file.
CREATE TABLE nfo_write_preparations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 actor_id uuid NOT NULL REFERENCES users(id),
 idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 128 AND idempotency_key ~ '^[!-~]+$'),
 version integer NOT NULL CHECK(version=1),
 request_bytes bytea NOT NULL CHECK(octet_length(request_bytes) BETWEEN 1 AND 33554432),
 request_digest text NOT NULL CHECK(request_digest=encode(sha256(request_bytes),'hex')),
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
 item_id uuid NOT NULL,
 source_id uuid NOT NULL,
 root_id uuid NOT NULL,
 kind text NOT NULL CHECK(kind IN ('Movie','HomeVideo','Series','Season','Episode')),
 revision bigint NOT NULL CHECK(revision BETWEEN 1 AND 2147483646),
 generation bigint NOT NULL CHECK(generation>0),
 root_path text NOT NULL CHECK(octet_length(root_path) BETWEEN 1 AND 32768),
 relative_path text NOT NULL CHECK(octet_length(relative_path) BETWEEN 1 AND 1024),
 media_path text NOT NULL CHECK(octet_length(media_path)<=1024),
 directory_path text NOT NULL CHECK(octet_length(directory_path)<=1024),
 max_bytes bigint NOT NULL CHECK(max_bytes BETWEEN 1 AND 33554432),
 modified_unix_nano bigint NOT NULL,
 original_bytes bytea NOT NULL CHECK(octet_length(original_bytes) BETWEEN 1 AND max_bytes),
 original_sha256 text NOT NULL CHECK(original_sha256=encode(sha256(original_bytes),'hex')),
 replacement_bytes bytea NOT NULL CHECK(octet_length(replacement_bytes) BETWEEN 1 AND max_bytes),
 replacement_sha256 text NOT NULL CHECK(replacement_sha256=encode(sha256(replacement_bytes),'hex')),
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP CHECK(isfinite(created_at)),
 expires_at timestamptz NOT NULL DEFAULT (CURRENT_TIMESTAMP+interval '24 hours') CHECK(isfinite(expires_at)),
 CHECK(expires_at=created_at+interval '24 hours'),
 CHECK(jsonb_typeof(convert_from(request_bytes,'UTF8')::jsonb)='object'
  AND convert_from(request_bytes,'UTF8')::jsonb ?& ARRAY['itemId','revision','edits','createMissing','bom','indent','maxBytes','backups']),
 CHECK((convert_from(request_bytes,'UTF8')::jsonb->>'itemId')::uuid IS NOT NULL
  AND (convert_from(request_bytes,'UTF8')::jsonb->>'revision')::bigint IS NOT NULL
  AND (convert_from(request_bytes,'UTF8')::jsonb->>'maxBytes')::bigint IS NOT NULL
  AND (convert_from(request_bytes,'UTF8')::jsonb->>'itemId')::uuid=item_id
  AND (convert_from(request_bytes,'UTF8')::jsonb->>'revision')::bigint=revision
  AND (convert_from(request_bytes,'UTF8')::jsonb->>'maxBytes')::bigint=max_bytes),
 CHECK((directory_path='' AND media_path<>'') OR (directory_path<>'' AND media_path='' AND kind IN ('Series','Season'))),
 UNIQUE(actor_id,idempotency_key),
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE
);
CREATE INDEX nfo_write_preparations_expiry_idx ON nfo_write_preparations(expires_at,id);
CREATE INDEX nfo_write_preparations_library_idx ON nfo_write_preparations(library_id);

CREATE FUNCTION guard_nfo_write_preparation() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE total_rows bigint; actor_rows bigint; library_bytes bigint; total_bytes bigint; incoming bigint;
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW IS DISTINCT FROM OLD THEN
   RAISE EXCEPTION 'nfo write preparation is immutable' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 PERFORM pg_advisory_xact_lock(hashtext(TG_TABLE_SCHEMA),17481247);
 EXECUTE format('SELECT count(*),count(*) FILTER(WHERE actor_id=$1),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)),0),COALESCE(sum(octet_length(request_bytes)::bigint+octet_length(original_bytes)+octet_length(replacement_bytes)) FILTER(WHERE library_id=$2),0) FROM %I.nfo_write_preparations',TG_TABLE_SCHEMA)
 INTO total_rows,actor_rows,total_bytes,library_bytes USING NEW.actor_id,NEW.library_id;
 incoming:=octet_length(NEW.request_bytes)::bigint+octet_length(NEW.original_bytes)+octet_length(NEW.replacement_bytes);
 IF total_rows>=256 OR actor_rows>=32 OR total_bytes+incoming>268435456 OR library_bytes+incoming>134217728 THEN
  RAISE EXCEPTION 'nfo write preparation capacity reached' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_nfo_write_preparation BEFORE INSERT OR UPDATE ON nfo_write_preparations
 FOR EACH ROW EXECUTE FUNCTION guard_nfo_write_preparation();
COMMIT;
