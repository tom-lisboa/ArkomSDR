-- Version 4: outbound voice is isolated from the messaging job queue.
CREATE TABLE IF NOT EXISTS bia_go.voice_requests (
 id text PRIMARY KEY, lead_id text NOT NULL REFERENCES bia_go.leads(id),
 mode text NOT NULL CHECK(mode IN ('shadow','live')), sender text NOT NULL,
 purpose text NOT NULL CHECK(purpose IN ('schedule','confirm','recover')),
 state text NOT NULL DEFAULT 'new', created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours', due_at timestamptz NOT NULL DEFAULT now(),
 lease_token text, lease_until timestamptz, permission_sid text,
 permission_at timestamptz, permission_expires_at timestamptz, permanent boolean NOT NULL DEFAULT false,
 revoked_at timestamptz, call_sid text UNIQUE, callback_seq bigint NOT NULL DEFAULT -1,
 stream_started boolean NOT NULL DEFAULT false, realtime_session_id text,
 offers jsonb NOT NULL DEFAULT '[]', error_code text, updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS bia_go_voice_one_active ON bia_go.voice_requests(lead_id,mode)
 WHERE state IN ('new','requesting','awaiting_permission','ready','dialing','queued','initiated','ringing','in-progress','uncertain');
CREATE INDEX IF NOT EXISTS bia_go_voice_due ON bia_go.voice_requests(due_at) WHERE state IN ('new','ready');
CREATE TABLE IF NOT EXISTS bia_go.voice_webhooks (
 id text PRIMARY KEY, occurred_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS bia_go.voice_transcripts (
 request_id text NOT NULL REFERENCES bia_go.voice_requests(id), item_id text NOT NULL,
 role text NOT NULL CHECK(role IN ('user','assistant')), body text NOT NULL,
 seq bigint GENERATED ALWAYS AS IDENTITY, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(request_id,item_id,role)
);
INSERT INTO bia_go.schema_versions(version) VALUES(4) ON CONFLICT DO NOTHING;
