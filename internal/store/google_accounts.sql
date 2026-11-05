-- Many Google identities per seller, with many calendars per Google identity.
CREATE TABLE IF NOT EXISTS bia_go.google_accounts (
 id text PRIMARY KEY,
 user_id text NOT NULL REFERENCES bia_go.management_users(id) ON DELETE CASCADE,
 client_id text NOT NULL,
 google_subject text,
 google_email text NOT NULL DEFAULT '',
 credential bytea NOT NULL,
 credential_version integer NOT NULL DEFAULT 2 CHECK(credential_version IN (1,2)),
 scopes text[] NOT NULL,
 expires_at timestamptz NOT NULL,
 connected_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 UNIQUE(user_id,client_id,google_subject)
);
CREATE INDEX IF NOT EXISTS google_accounts_user ON bia_go.google_accounts(user_id,client_id,id);
ALTER TABLE bia_go.google_oauth_states ADD COLUMN IF NOT EXISTS account_id text REFERENCES bia_go.google_accounts(id) ON DELETE CASCADE;
-- Copy v20 grants once; never resurrect a subsequently disconnected account.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM bia_go.schema_versions WHERE version=21) THEN
  INSERT INTO bia_go.google_accounts(id,user_id,client_id,credential,credential_version,scopes,expires_at,connected_at,updated_at)
  SELECT 'gac_legacy_'||md5(user_id),user_id,client_id,credential,1,scopes,expires_at,connected_at,updated_at
  FROM bia_go.google_connections ON CONFLICT DO NOTHING;
 END IF;
END $$;
INSERT INTO bia_go.schema_versions(version) VALUES(21) ON CONFLICT DO NOTHING;
