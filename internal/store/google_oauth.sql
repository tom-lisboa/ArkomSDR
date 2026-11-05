-- A Google connection belongs to a staff user, not to the public form or a lead.
CREATE TABLE IF NOT EXISTS bia_go.google_oauth_states (
 state_hash text PRIMARY KEY,
 user_id text NOT NULL REFERENCES bia_go.management_users(id) ON DELETE CASCADE,
 session_hash text NOT NULL REFERENCES bia_go.management_sessions(token_hash) ON DELETE CASCADE,
 client_id text NOT NULL,
 verifier bytea NOT NULL,
 browser_hash text,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 started_at timestamptz
);
ALTER TABLE bia_go.google_oauth_states ADD COLUMN IF NOT EXISTS consumed_at timestamptz;
CREATE INDEX IF NOT EXISTS google_oauth_states_expiry ON bia_go.google_oauth_states(expires_at);
CREATE INDEX IF NOT EXISTS google_oauth_states_user ON bia_go.google_oauth_states(user_id);
CREATE TABLE IF NOT EXISTS bia_go.google_connections (
 user_id text PRIMARY KEY REFERENCES bia_go.management_users(id) ON DELETE CASCADE,
 client_id text NOT NULL,
 credential bytea NOT NULL,
 scopes text[] NOT NULL,
 expires_at timestamptz NOT NULL,
 connected_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL
);
INSERT INTO bia_go.schema_versions(version) VALUES(20) ON CONFLICT DO NOTHING;
