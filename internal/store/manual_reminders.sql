-- Version 11: distinguish explicit demo calls from scheduled reminders.
ALTER TABLE bia_go.voice_requests ADD COLUMN IF NOT EXISTS manual_reminder boolean NOT NULL DEFAULT false;
INSERT INTO bia_go.schema_versions(version) VALUES(11) ON CONFLICT DO NOTHING;
