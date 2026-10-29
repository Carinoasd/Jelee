BEGIN;
-- Migration 000089 added jobs.export_job_id (the export job a child nfo_write
-- job belongs to) with only a kind check on the child. Nothing stopped it from
-- naming a missing job, a job of another kind or a job of another library.
-- Bind it to an nfo_export job of the same library.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;

-- References that cannot satisfy the new rule are history (an export job that
-- history trim already removed, which until now left the pointer dangling).
-- Clearing them keeps the child and does not touch its state, owner or journal.
UPDATE jobs c SET export_job_id=NULL
 WHERE c.export_job_id IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM jobs p WHERE p.id=c.export_job_id AND p.kind='nfo_export' AND p.library_id=c.library_id);
-- The write-job trigger is deferred; ALTER TABLE refuses to run with its
-- events pending, so let it check the updated rows now.
SET CONSTRAINTS verify_nfo_write_job IMMEDIATE;

-- ON DELETE SET NULL: history trim deletes export jobs by age, and a child
-- write job whose commit journal is retained cannot be deleted
-- (retain_nfo_write_commit_job). CASCADE would make that trim fail, and
-- RESTRICT would make it fail whenever the export is older than a retained
-- child. The child keeps its own history and only loses the pointer. The
-- update leaves state, owner and identity alone, so the retention trigger
-- accepts it.
ALTER TABLE jobs ADD CONSTRAINT jobs_export_job_fkey FOREIGN KEY(export_job_id) REFERENCES jobs(id) ON DELETE SET NULL;
CREATE INDEX jobs_export_job_idx ON jobs(export_job_id) WHERE export_job_id IS NOT NULL;

-- The foreign key only proves the job exists. A child must name an
-- nfo_export job of its own library. A missing parent is left to the foreign
-- key, which reports it at the end of the statement.
CREATE FUNCTION guard_job_export_parent() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE parent_kind text; parent_library uuid;
BEGIN
 EXECUTE format('SELECT kind,library_id FROM %I.jobs WHERE id=$1 FOR KEY SHARE',TG_TABLE_SCHEMA)
 INTO parent_kind,parent_library USING NEW.export_job_id;
 IF parent_kind IS NULL THEN RETURN NEW; END IF;
 IF parent_kind<>'nfo_export' OR parent_library<>NEW.library_id THEN
  RAISE EXCEPTION 'export job reference must name an nfo export job of the same library' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_job_export_parent BEFORE INSERT OR UPDATE OF export_job_id,library_id ON jobs
 FOR EACH ROW WHEN (NEW.export_job_id IS NOT NULL) EXECUTE FUNCTION guard_job_export_parent();

-- An export job that has children keeps its kind and library.
CREATE FUNCTION guard_job_export_children() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE referenced boolean;
BEGIN
 EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.jobs WHERE export_job_id=$1)',TG_TABLE_SCHEMA)
 INTO referenced USING OLD.id;
 IF referenced THEN
  RAISE EXCEPTION 'an export job with child jobs keeps its kind and library' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_job_export_children BEFORE UPDATE OF kind,library_id ON jobs
 FOR EACH ROW WHEN (OLD.kind IS DISTINCT FROM NEW.kind OR OLD.library_id IS DISTINCT FROM NEW.library_id)
 EXECUTE FUNCTION guard_job_export_children();
COMMIT;
