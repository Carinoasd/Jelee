BEGIN;
ALTER TABLE libraries ADD COLUMN nfo_mode text NOT NULL DEFAULT 'off' CHECK(nfo_mode IN ('off','read-only'));
ALTER TABLE libraries ADD COLUMN nfo_generation bigint NOT NULL DEFAULT 1 CHECK(nfo_generation>0);
ALTER TABLE library_roots ADD COLUMN nfo_generation bigint NOT NULL DEFAULT 1 CHECK(nfo_generation>0);

CREATE TABLE nfo_cache_quota (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 global_row_limit bigint NOT NULL CHECK(global_row_limit BETWEEN 1000 AND 1000000),
 global_byte_limit bigint NOT NULL CHECK(global_byte_limit BETWEEN 16777216 AND 17179869184),
 library_row_limit bigint NOT NULL CHECK(library_row_limit BETWEEN 1000 AND 500000 AND library_row_limit<=global_row_limit),
 library_byte_limit bigint NOT NULL CHECK(library_byte_limit BETWEEN 16777216 AND global_byte_limit),
 library_limit integer NOT NULL CHECK(library_limit BETWEEN 1 AND 1024),
 positive_ttl_seconds bigint NOT NULL CHECK(positive_ttl_seconds BETWEEN 3600 AND 7776000),
 negative_ttl_seconds bigint NOT NULL CHECK(negative_ttl_seconds BETWEEN 60 AND 86400 AND negative_ttl_seconds<=positive_ttl_seconds),
 rows_used bigint NOT NULL DEFAULT 0 CHECK(rows_used BETWEEN 0 AND global_row_limit),
 bytes_used bigint NOT NULL DEFAULT 0 CHECK(bytes_used BETWEEN 0 AND global_byte_limit),
 library_scopes integer NOT NULL DEFAULT 0 CHECK(library_scopes BETWEEN 0 AND library_limit)
);
CREATE TABLE nfo_library_quota (
 library_id uuid PRIMARY KEY REFERENCES libraries(id) ON DELETE RESTRICT,
 row_limit bigint NOT NULL CHECK(row_limit BETWEEN 1000 AND 500000),
 byte_limit bigint NOT NULL CHECK(byte_limit BETWEEN 16777216 AND 17179869184),
 rows_used bigint NOT NULL DEFAULT 0 CHECK(rows_used BETWEEN 0 AND row_limit),
 bytes_used bigint NOT NULL DEFAULT 0 CHECK(bytes_used BETWEEN 0 AND byte_limit)
);
CREATE TABLE nfo_policy_requests (
 actor_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 128 AND idempotency_key ~ '^[!-~]+$'),
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE RESTRICT,
 requested_mode text NOT NULL CHECK(requested_mode IN ('off','read-only')),
 expected_generation bigint NOT NULL CHECK(expected_generation>0),
 result_generation bigint NOT NULL CHECK(result_generation>=expected_generation),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '24 hours',
 PRIMARY KEY(actor_id,idempotency_key),
 CHECK(expires_at>created_at AND expires_at<=created_at+interval '24 hours 1 second')
);
CREATE INDEX nfo_policy_requests_expiry_idx ON nfo_policy_requests(expires_at,actor_id,idempotency_key);

CREATE TABLE nfo_job_state (
 job_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 mode text NOT NULL CHECK(mode IN ('off','read-only')),
 phase text NOT NULL CHECK(phase IN ('waiting','running','done','aborted')),
 parser_version text NOT NULL CHECK(octet_length(parser_version) BETWEEN 1 AND 64),
 summary_schema_version integer NOT NULL CHECK(summary_schema_version BETWEEN 1 AND 1000),
 fingerprint_version text NOT NULL CHECK(fingerprint_version='sha256-full-v1'),
 max_source_bytes bigint NOT NULL CHECK(max_source_bytes BETWEEN 1 AND 33554432),
 identity_digest bytea NOT NULL CHECK(octet_length(identity_digest)=32),
 library_generation bigint NOT NULL CHECK(library_generation>0),
 cursor_inventory_id uuid,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 processed bigint NOT NULL DEFAULT 0 CHECK(processed BETWEEN 0 AND 500000),
 hits bigint NOT NULL DEFAULT 0 CHECK(hits>=0),
 negative_hits bigint NOT NULL DEFAULT 0 CHECK(negative_hits>=0),
 parsed bigint NOT NULL DEFAULT 0 CHECK(parsed>=0),
 valid bigint NOT NULL DEFAULT 0 CHECK(valid>=0),
 invalid bigint NOT NULL DEFAULT 0 CHECK(invalid>=0),
 warning_files bigint NOT NULL DEFAULT 0 CHECK(warning_files>=0),
 changed bigint NOT NULL DEFAULT 0 CHECK(changed>=0),
 unavailable bigint NOT NULL DEFAULT 0 CHECK(unavailable>=0),
 rejected bigint NOT NULL DEFAULT 0 CHECK(rejected>=0),
 error_code text NOT NULL DEFAULT '' CHECK(error_code IN ('','nfo_disabled','nfo_invalidated','nfo_cache_capacity','nfo_identity_mismatch','nfo_unavailable','nfo_cancelled')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(job_id,library_id) REFERENCES jobs(id,library_id) ON DELETE CASCADE,
 CHECK(processed=hits+negative_hits+parsed+changed+unavailable+rejected),
 CHECK(valid+invalid=hits+negative_hits+parsed),
 CHECK(warning_files<=valid+invalid),
 CHECK((phase='aborted')=(error_code<>'')),
 CHECK(mode<>'off' OR (phase='aborted' AND error_code='nfo_disabled' AND processed=0 AND cursor_inventory_id IS NULL))
);
CREATE INDEX nfo_job_state_library_idx ON nfo_job_state(library_id);
CREATE FUNCTION nfo_phase_identity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.job_id,NEW.library_id,NEW.mode,NEW.parser_version,NEW.summary_schema_version,NEW.fingerprint_version,NEW.max_source_bytes,NEW.identity_digest,NEW.library_generation,NEW.created_at)
    IS DISTINCT FROM ROW(OLD.job_id,OLD.library_id,OLD.mode,OLD.parser_version,OLD.summary_schema_version,OLD.fingerprint_version,OLD.max_source_bytes,OLD.identity_digest,OLD.library_generation,OLD.created_at) THEN
  RAISE EXCEPTION 'nfo phase identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER nfo_phase_identity_immutable BEFORE UPDATE ON nfo_job_state FOR EACH ROW EXECUTE FUNCTION nfo_phase_identity_immutable();

CREATE TABLE nfo_cache (
 root_id uuid NOT NULL,
 relative_path text NOT NULL CHECK(octet_length(relative_path) BETWEEN 1 AND 1024 AND relative_path !~ '[[:cntrl:]]' AND relative_path !~ '(^|/)\.{1,2}(/|$)' AND relative_path NOT LIKE '/%' AND relative_path NOT LIKE '%/' AND position('//' in relative_path)=0 AND position(E'\\' in relative_path)=0 AND position(':' in relative_path)=0 AND cardinality(string_to_array(relative_path,'/'))<=128),
 library_id uuid NOT NULL,
 size bigint NOT NULL CHECK(size BETWEEN 0 AND 33554432),
 modified_unix_nano bigint NOT NULL,
 source_sha256 bytea NOT NULL CHECK(octet_length(source_sha256)=32),
 fingerprint_version text NOT NULL CHECK(fingerprint_version='sha256-full-v1'),
 identity_digest bytea NOT NULL CHECK(octet_length(identity_digest)=32),
 library_generation bigint NOT NULL CHECK(library_generation>0),
 root_generation bigint NOT NULL CHECK(root_generation>0),
 status text NOT NULL CHECK(status IN ('valid','invalid')),
 summary jsonb NOT NULL CHECK(jsonb_typeof(summary)='object' AND octet_length(summary::text)<=16384),
 expires_at timestamptz NOT NULL,
 charge_bytes bigint NOT NULL CHECK(charge_bytes=2048+octet_length(summary::text)),
 last_used_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(root_id,relative_path),
 FOREIGN KEY(root_id,library_id) REFERENCES library_roots(id,library_id) ON DELETE RESTRICT,
 FOREIGN KEY(library_id) REFERENCES nfo_library_quota(library_id) ON DELETE RESTRICT,
 CHECK((summary->>'status') IS NOT DISTINCT FROM status)
);
CREATE INDEX nfo_cache_expiry_idx ON nfo_cache(expires_at,root_id,relative_path);
CREATE INDEX nfo_cache_lru_idx ON nfo_cache(last_used_at,root_id,relative_path);
CREATE INDEX nfo_cache_library_expiry_idx ON nfo_cache(library_id,expires_at,root_id,relative_path);
CREATE INDEX nfo_cache_library_lru_idx ON nfo_cache(library_id,last_used_at,root_id,relative_path);
CREATE INDEX job_inventory_nfo_page_idx ON job_inventory(job_id,id) WHERE kind='nfo';

-- Separate from the media probe trigger: changing NFO mode never invalidates
-- video probe observations. Root changes invalidate both independent systems.
CREATE FUNCTION nfo_root_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target uuid;
BEGIN
 IF TG_OP='UPDATE' AND NEW.path IS NOT DISTINCT FROM OLD.path AND NEW.library_id=OLD.library_id THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' THEN NEW.nfo_generation=OLD.nfo_generation+1; END IF;
 FOR target IN SELECT id FROM libraries WHERE id IN (CASE WHEN TG_OP<>'INSERT' THEN OLD.library_id END,CASE WHEN TG_OP<>'DELETE' THEN NEW.library_id END) ORDER BY id LOOP
  UPDATE libraries SET nfo_generation=nfo_generation+1 WHERE id=target;
 END LOOP;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER nfo_root_changed BEFORE INSERT OR DELETE OR UPDATE OF path,library_id ON library_roots FOR EACH ROW EXECUTE FUNCTION nfo_root_changed();
COMMIT;
