BEGIN;
LOCK TABLE client_control_policy, client_rules, known_clients, known_client_sessions, client_control_hits IN ACCESS EXCLUSIVE MODE;
-- Schema 69 has no client control. Dropping an enforcing rule or a strict
-- unknown-client policy would silently admit what it refuses, so they block
-- the downgrade; disabling them first (jelee-cli access reset-policies) is
-- an explicit operator decision. Observe-only rules, known clients and hit
-- records are dropped with the schema.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM client_rules WHERE enabled AND action NOT IN ('observe','shadow'))
  OR EXISTS(SELECT 1 FROM client_control_policy WHERE unknown_clients<>'allow') THEN
  RAISE EXCEPTION 'enforcing client control rules prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE client_control_hits;
DROP TABLE known_client_sessions;
DROP TABLE known_clients;
DROP TABLE client_rules;
DROP TABLE client_control_policy;
COMMIT;
