-- Version 8: scoped test-page submissions, claimed by signed WhatsApp input.
CREATE TABLE IF NOT EXISTS bia_go.test_forms (
 id text PRIMARY KEY,
 code text NOT NULL UNIQUE,
 phone text NOT NULL,
 profile jsonb NOT NULL,
 fingerprint text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
 claimed_by text,
 claimed_at timestamptz
);
CREATE INDEX IF NOT EXISTS bia_go_test_forms_created ON bia_go.test_forms(created_at);
INSERT INTO bia_go.schema_versions(version) VALUES(8) ON CONFLICT DO NOTHING;
