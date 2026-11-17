BEGIN;
-- The layout cache is rebuilt by the next catalog sync after an upgrade;
-- dropping it loses no administrator decision.
DROP TABLE media_source_layouts;
COMMIT;
