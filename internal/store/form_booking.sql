-- Version 13: optional calendar selection attached to a form submission.
ALTER TABLE bia_go.test_forms ADD COLUMN IF NOT EXISTS selected_slot_id text NOT NULL DEFAULT '';
INSERT INTO bia_go.schema_versions(version) VALUES(13) ON CONFLICT DO NOTHING;
