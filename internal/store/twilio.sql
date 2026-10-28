CREATE TABLE IF NOT EXISTS bia_go.twilio_messages (
 id text PRIMARY KEY,
 phone text NOT NULL,
 fingerprint text NOT NULL,
 sid text UNIQUE,
 status text NOT NULL DEFAULT 'initiating',
 error_code text,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO bia_go.schema_versions(version) VALUES(5) ON CONFLICT DO NOTHING;
