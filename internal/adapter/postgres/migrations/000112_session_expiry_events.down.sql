BEGIN;
-- The watermark only spaces webhook events; dropping it loses no data.
DROP INDEX sessions_expiry_idx;
DROP TABLE session_expiry_watermark;
COMMIT;
