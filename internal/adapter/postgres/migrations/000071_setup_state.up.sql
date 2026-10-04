BEGIN;
-- G18 initial setup. One row holds the wizard progress; the JSON document is
-- the step data (never a password, hash, session or TMDB credential) while
-- version, current step and completion live in columns the gate reads.
CREATE TABLE setup_state (
 id smallint PRIMARY KEY DEFAULT 1 CHECK (id=1),
 version bigint NOT NULL CHECK (version>0),
 current_step text NOT NULL CHECK (current_step IN ('language','admin','database','media','tmdb','toolchain','metadata-policy','network','complete')),
 state jsonb NOT NULL CHECK (jsonb_typeof(state)='object' AND octet_length(state::text)<=262144),
 -- adopted marks an installation that already served before the wizard
 -- existed; it never ran the wizard and counts as completed.
 adopted boolean NOT NULL DEFAULT false,
 completed_at timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (completed_at IS NULL OR current_step='complete'),
 CHECK (NOT adopted OR completed_at IS NOT NULL)
);

-- A completed setup is final. The repository already refuses such writes;
-- this is the backstop for any other writer.
CREATE FUNCTION setup_state_final() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.completed_at IS NOT NULL THEN
  RAISE EXCEPTION 'completed setup state is final' USING ERRCODE='23514';
 END IF;
 IF TG_OP='DELETE' THEN
  RETURN OLD;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER setup_state_final BEFORE UPDATE OR DELETE ON setup_state
 FOR EACH ROW EXECUTE FUNCTION setup_state_final();

-- Upgrading an installation that already has accounts or libraries must not
-- lock it behind the wizard: record it as completed (adopted) right away.
INSERT INTO setup_state(id,version,current_step,state,adopted,completed_at)
SELECT 1,1,'complete','{}'::jsonb,true,now()
WHERE EXISTS(SELECT 1 FROM users WHERE deleted_at IS NULL) OR EXISTS(SELECT 1 FROM libraries);
COMMIT;
