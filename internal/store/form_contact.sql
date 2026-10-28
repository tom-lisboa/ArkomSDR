-- Version 10: the form can request contact without a WhatsApp pairing message.
ALTER TABLE bia_go.test_forms ADD COLUMN IF NOT EXISTS contact_event_id text;
INSERT INTO bia_go.schema_versions(version) VALUES(10) ON CONFLICT DO NOTHING;
