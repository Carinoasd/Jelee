BEGIN;

ALTER TABLE libraries ADD COLUMN probe_generation bigint NOT NULL DEFAULT 1 CHECK (probe_generation>0);
ALTER TABLE library_roots ADD COLUMN probe_generation bigint NOT NULL DEFAULT 1 CHECK (probe_generation>0);
ALTER TABLE items ADD COLUMN probe_generation bigint NOT NULL DEFAULT 1 CHECK (probe_generation>0);
ALTER TABLE jobs ADD CONSTRAINT jobs_id_library_unique UNIQUE(id,library_id);

-- Sequence values are never reused by expiry, eviction, rollback or row
-- recreation. A cached row's local counter would allow a stale-lease ABA.
CREATE SEQUENCE probe_lease_fence_seq AS bigint NO CYCLE;

CREATE TABLE probe_cache_quota (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 global_row_limit bigint NOT NULL CHECK (global_row_limit BETWEEN 1000 AND 1000000),
 library_row_limit bigint NOT NULL CHECK (library_row_limit BETWEEN 1000 AND 500000 AND library_row_limit<=global_row_limit),
 global_byte_limit bigint NOT NULL CHECK (global_byte_limit BETWEEN 16777216 AND 17179869184),
 library_byte_limit bigint NOT NULL CHECK (library_byte_limit BETWEEN 16777216 AND global_byte_limit),
 active_lease_limit integer NOT NULL CHECK (active_lease_limit BETWEEN 1 AND 8),
 library_limit integer NOT NULL CHECK (library_limit BETWEEN 1 AND 1024),
 tool_limit integer NOT NULL CHECK (tool_limit BETWEEN 1 AND 32),
 positive_ttl_seconds bigint NOT NULL CHECK (positive_ttl_seconds BETWEEN 3600 AND 7776000),
 negative_ttl_seconds bigint NOT NULL CHECK (negative_ttl_seconds BETWEEN 60 AND 86400),
 lease_seconds bigint NOT NULL CHECK (lease_seconds BETWEEN 10 AND 300),
 rows_used bigint NOT NULL DEFAULT 0 CHECK (rows_used BETWEEN 0 AND global_row_limit),
 bytes_used bigint NOT NULL DEFAULT 0 CHECK (bytes_used BETWEEN 0 AND global_byte_limit),
 active_leases integer NOT NULL DEFAULT 0 CHECK (active_leases BETWEEN 0 AND active_lease_limit),
 library_scopes integer NOT NULL DEFAULT 0 CHECK (library_scopes BETWEEN 0 AND library_limit),
 tools_used integer NOT NULL DEFAULT 0 CHECK (tools_used BETWEEN 0 AND tool_limit)
);
-- Deliberately not seeded. EnsureProbePolicy initializes once under the jobs
-- lock; a different instance cannot silently raise established cluster limits.
CREATE TABLE probe_library_quota (
 library_id uuid PRIMARY KEY REFERENCES libraries(id) ON DELETE RESTRICT,
 row_limit bigint NOT NULL CHECK (row_limit BETWEEN 1000 AND 500000),
 byte_limit bigint NOT NULL CHECK (byte_limit BETWEEN 16777216 AND 17179869184),
 rows_used bigint NOT NULL DEFAULT 0 CHECK (rows_used BETWEEN 0 AND row_limit),
 bytes_used bigint NOT NULL DEFAULT 0 CHECK (bytes_used BETWEEN 0 AND byte_limit),
 active_leases integer NOT NULL DEFAULT 0 CHECK (active_leases BETWEEN 0 AND 8)
);

CREATE TABLE tool_versions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 identity_digest bytea NOT NULL UNIQUE CHECK (octet_length(identity_digest)=32),
 platform text NOT NULL CHECK (platform='linux-amd64'),
 vendor_version text NOT NULL CHECK (octet_length(vendor_version) BETWEEN 1 AND 256),
 upstream_version text NOT NULL CHECK (octet_length(upstream_version) BETWEEN 1 AND 64),
 source_revision text NOT NULL CHECK (octet_length(source_revision) BETWEEN 1 AND 128),
 executable_sha256 bytea NOT NULL CHECK (octet_length(executable_sha256)=32),
 runtime_sha256 bytea NOT NULL CHECK (octet_length(runtime_sha256)=32),
 parser_version text NOT NULL CHECK (octet_length(parser_version) BETWEEN 1 AND 64),
 metadata_schema_version integer NOT NULL CHECK (metadata_schema_version BETWEEN 1 AND 1000),
 sandbox_version text NOT NULL CHECK (octet_length(sandbox_version) BETWEEN 1 AND 64),
 arguments_sha256 bytea NOT NULL CHECK (octet_length(arguments_sha256)=32),
 fingerprint_version text NOT NULL CHECK (fingerprint_version='edge-sha256-v1'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION probe_identity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'probe identity is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER probe_identity_immutable BEFORE UPDATE ON tool_versions
 FOR EACH ROW EXECUTE FUNCTION probe_identity_immutable();

CREATE TABLE probe_job_state (
 job_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 tool_version_id uuid NOT NULL REFERENCES tool_versions(id) ON DELETE RESTRICT,
 phase text NOT NULL CHECK (phase IN ('running','done','aborted')),
 scope text NOT NULL CHECK (scope IN ('incremental','library_rebuild','item_rebuild')),
 library_generation bigint NOT NULL CHECK (library_generation>0),
 target_item_id uuid,
 target_item_generation bigint,
 cursor_inventory_id uuid,
 revision bigint NOT NULL DEFAULT 1 CHECK (revision>0),
 processed bigint NOT NULL DEFAULT 0 CHECK (processed BETWEEN 0 AND 500000),
 hits bigint NOT NULL DEFAULT 0 CHECK (hits>=0),
 negative_hits bigint NOT NULL DEFAULT 0 CHECK (negative_hits>=0),
 succeeded bigint NOT NULL DEFAULT 0 CHECK (succeeded>=0),
 failed bigint NOT NULL DEFAULT 0 CHECK (failed>=0),
 changed bigint NOT NULL DEFAULT 0 CHECK (changed>=0),
 unavailable bigint NOT NULL DEFAULT 0 CHECK (unavailable>=0),
 error_code text NOT NULL DEFAULT '' CHECK (error_code IN ('','probe_runtime_unavailable','probe_cache_capacity','probe_invalidated','probe_identity_mismatch')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(job_id,library_id) REFERENCES jobs(id,library_id) ON DELETE CASCADE,
 FOREIGN KEY(target_item_id,library_id) REFERENCES items(id,library_id) ON DELETE RESTRICT,
 CHECK ((scope='item_rebuild')=(target_item_id IS NOT NULL)),
 CHECK ((target_item_id IS NULL)=(target_item_generation IS NULL)),
 CHECK (target_item_generation IS NULL OR target_item_generation>0),
 CHECK (processed=hits+negative_hits+succeeded+failed+changed+unavailable),
 CHECK ((phase='aborted')=(error_code<>''))
);
CREATE INDEX probe_phase_tool_idx ON probe_job_state(tool_version_id);
CREATE INDEX probe_phase_target_idx ON probe_job_state(target_item_id);

CREATE TABLE probe_cache (
 root_id uuid NOT NULL,
 relative_path text NOT NULL CHECK (
  octet_length(relative_path) BETWEEN 1 AND 1024 AND
  relative_path !~ '[[:cntrl:]]' AND relative_path !~ '(^|/)\.{1,2}(/|$)' AND
  relative_path NOT LIKE '/%' AND relative_path NOT LIKE '%/' AND
  position('//' in relative_path)=0 AND position(E'\\' in relative_path)=0 AND
  position(':' in relative_path)=0 AND cardinality(string_to_array(relative_path,'/'))<=128),
 library_id uuid NOT NULL,
 item_id uuid,
 size bigint NOT NULL CHECK (size>=0),
 modified_unix_nano bigint NOT NULL,
 fingerprint bytea NOT NULL CHECK (octet_length(fingerprint)=32),
 fingerprint_version text NOT NULL CHECK (fingerprint_version='edge-sha256-v1'),
 tool_version_id uuid NOT NULL REFERENCES tool_versions(id) ON DELETE RESTRICT,
 library_generation bigint NOT NULL CHECK (library_generation>0),
 root_generation bigint NOT NULL CHECK (root_generation>0),
 item_generation bigint,
 state text NOT NULL CHECK (state IN ('pending','ready','failed')),
 metadata jsonb,
 error_code text NOT NULL DEFAULT '' CHECK (error_code IN ('','probe_failed','probe_metadata_invalid','probe_metadata_limit','probe_output_limit','probe_timeout')),
 expires_at timestamptz,
 retry_after timestamptz,
 failure_count integer NOT NULL DEFAULT 0 CHECK (failure_count BETWEEN 0 AND 10),
 lease_owner text,
 lease_generation bigint,
 lease_until timestamptz,
 lease_job_id uuid,
 lease_job_generation bigint,
 charge_bytes bigint NOT NULL CHECK (charge_bytes BETWEEN 2048 AND 133120),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_used_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(root_id,relative_path),
 FOREIGN KEY(root_id,library_id) REFERENCES library_roots(id,library_id) ON DELETE RESTRICT,
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE RESTRICT,
 FOREIGN KEY(library_id) REFERENCES probe_library_quota(library_id) ON DELETE RESTRICT,
 FOREIGN KEY(lease_job_id,library_id) REFERENCES jobs(id,library_id) ON DELETE RESTRICT,
 CHECK ((item_id IS NULL)=(item_generation IS NULL)),
 CHECK (item_generation IS NULL OR item_generation>0),
 CHECK ((state='ready')=(metadata IS NOT NULL)),
 CHECK (metadata IS NULL OR (jsonb_typeof(metadata)='object' AND octet_length(metadata::text)<=131072)),
 CHECK ((state='failed')=(error_code<>'')),
 CHECK ((state='failed')=(retry_after IS NOT NULL)),
 CHECK ((state='pending')=(expires_at IS NULL)),
 CHECK (state<>'ready' OR failure_count=0),
 CHECK (state<>'failed' OR failure_count>=1),
 CHECK ((lease_owner IS NOT NULL)=(lease_generation IS NOT NULL)),
 CHECK ((lease_owner IS NOT NULL)=(lease_until IS NOT NULL)),
 CHECK ((lease_owner IS NOT NULL)=(lease_job_id IS NOT NULL)),
 CHECK ((lease_owner IS NOT NULL)=(lease_job_generation IS NOT NULL)),
 CHECK (lease_owner IS NULL OR (state='pending' AND octet_length(lease_owner) BETWEEN 1 AND 128 AND lease_generation>0 AND lease_job_generation>0)),
 CHECK (charge_bytes=2048+CASE WHEN state='ready' THEN octet_length(metadata::text) ELSE 0 END+CASE WHEN lease_owner IS NOT NULL THEN 131072 ELSE 0 END)
);
CREATE INDEX probe_cache_library_idx ON probe_cache(library_id,updated_at,root_id,relative_path);
CREATE INDEX probe_cache_expiry_idx ON probe_cache(expires_at,root_id,relative_path) WHERE lease_owner IS NULL;
CREATE INDEX probe_cache_lease_idx ON probe_cache(lease_until) WHERE lease_owner IS NOT NULL;
CREATE UNIQUE INDEX probe_cache_parent_lease_idx ON probe_cache(lease_job_id) WHERE lease_job_id IS NOT NULL;
CREATE INDEX probe_cache_lru_idx ON probe_cache(last_used_at,root_id,relative_path) WHERE lease_owner IS NULL;
CREATE INDEX probe_cache_library_lru_idx ON probe_cache(library_id,last_used_at,root_id,relative_path) WHERE lease_owner IS NULL;
CREATE INDEX probe_cache_library_expiry_idx ON probe_cache(library_id,expires_at,root_id,relative_path) WHERE lease_owner IS NULL;
CREATE INDEX probe_cache_item_idx ON probe_cache(item_id);
CREATE INDEX probe_cache_tool_idx ON probe_cache(tool_version_id);

-- Every official writer obtains the jobs lock before touching scope rows.
-- These triggers preserve generation changes when a future writer is added.
CREATE FUNCTION probe_root_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target uuid;
BEGIN
 IF TG_OP='UPDATE' AND NEW.path IS NOT DISTINCT FROM OLD.path AND NEW.library_id=OLD.library_id THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' THEN NEW.probe_generation=OLD.probe_generation+1; END IF;
 FOR target IN SELECT id FROM libraries WHERE id IN (CASE WHEN TG_OP<>'INSERT' THEN OLD.library_id END,CASE WHEN TG_OP<>'DELETE' THEN NEW.library_id END) ORDER BY id LOOP
  UPDATE libraries SET probe_generation=probe_generation+1 WHERE id=target;
 END LOOP;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER probe_root_changed BEFORE INSERT OR DELETE OR UPDATE OF path,library_id ON library_roots
 FOR EACH ROW EXECUTE FUNCTION probe_root_changed();
CREATE FUNCTION probe_mapping_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target uuid;
BEGIN
 IF TG_OP='UPDATE' AND ROW(NEW.item_id,NEW.library_id,NEW.root_id,NEW.relative_path) IS NOT DISTINCT FROM ROW(OLD.item_id,OLD.library_id,OLD.root_id,OLD.relative_path) THEN RETURN NEW; END IF;
 FOR target IN SELECT id FROM items WHERE id IN (CASE WHEN TG_OP<>'INSERT' THEN OLD.item_id END,CASE WHEN TG_OP<>'DELETE' THEN NEW.item_id END) ORDER BY id LOOP
  UPDATE items SET probe_generation=probe_generation+1 WHERE id=target;
 END LOOP;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER probe_mapping_changed AFTER INSERT OR DELETE OR UPDATE OF item_id,library_id,root_id,relative_path ON media_sources
 FOR EACH ROW EXECUTE FUNCTION probe_mapping_changed();
COMMIT;
