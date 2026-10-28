-- Version 6: distributed best-effort typing throttling, owned by context.
ALTER TABLE bia_go.conversations ADD COLUMN IF NOT EXISTS typing_at timestamptz;
INSERT INTO bia_go.schema_versions(version) VALUES(6) ON CONFLICT DO NOTHING;
