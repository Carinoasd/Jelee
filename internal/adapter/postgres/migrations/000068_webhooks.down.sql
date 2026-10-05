BEGIN;
LOCK TABLE webhooks, webhook_outbox, webhook_deliveries, webhook_delivery_attempts IN ACCESS EXCLUSIVE MODE;
-- Endpoint configuration is operator data and the previous schema has nowhere to keep
-- it, so configured endpoints block the downgrade; deleting them is an
-- explicit operator decision. Queued events and the delivery log are
-- transient and go with the tables.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM webhooks) THEN
  RAISE EXCEPTION 'configured webhooks prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE webhook_delivery_attempts;
DROP TABLE webhook_deliveries;
DROP TABLE webhook_outbox;
DROP TABLE webhooks;
COMMIT;
