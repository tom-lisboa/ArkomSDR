-- Version 2: context and media own durable inboxes; execution retains jobs.
CREATE TABLE IF NOT EXISTS bia_go.worker_tasks (
 id text PRIMARY KEY, queue text NOT NULL CHECK(queue IN ('context','media')),
 kind text NOT NULL, lead_id text NOT NULL REFERENCES bia_go.leads(id),
 mode text NOT NULL CHECK(mode IN ('shadow','live')), payload jsonb NOT NULL,
 fingerprint text NOT NULL, status text NOT NULL DEFAULT 'pending',
 attempts integer NOT NULL DEFAULT 0, due_at timestamptz NOT NULL DEFAULT now(),
 lease_token text, lease_until timestamptz, error_code text, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS bia_go_worker_tasks_ready ON bia_go.worker_tasks(queue,due_at,created_at) WHERE status IN ('pending','running');
CREATE TABLE IF NOT EXISTS bia_go.conversations (
 id text PRIMARY KEY, lead_id text NOT NULL REFERENCES bia_go.leads(id),
 mode text NOT NULL, version bigint NOT NULL DEFAULT 0,
 heads jsonb NOT NULL DEFAULT '{}', active_run text,
 first_pending_at timestamptz, last_pending_at timestamptz,
 flush_at timestamptz, updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS bia_go_conversations_flush ON bia_go.conversations(flush_at) WHERE flush_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS bia_go.conversation_messages (
 id text PRIMARY KEY, conversation_id text NOT NULL REFERENCES bia_go.conversations(id),
 role text NOT NULL, body text NOT NULL, event jsonb,
 occurred_at timestamptz NOT NULL, received_at timestamptz NOT NULL DEFAULT now(),
 seq bigint GENERATED ALWAYS AS IDENTITY UNIQUE, run_id text
);
CREATE INDEX IF NOT EXISTS bia_go_messages_pending ON bia_go.conversation_messages(conversation_id,seq) WHERE role='user' AND run_id IS NULL;
CREATE INDEX IF NOT EXISTS bia_go_messages_history ON bia_go.conversation_messages(conversation_id,seq DESC);
CREATE TABLE IF NOT EXISTS bia_go.media_assets (
 id text PRIMARY KEY, conversation_id text NOT NULL REFERENCES bia_go.conversations(id),
 message_id text NOT NULL REFERENCES bia_go.conversation_messages(id),
 kind text NOT NULL, source_url text NOT NULL,
 status text NOT NULL DEFAULT 'pending', body text NOT NULL DEFAULT '',
 error_code text, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS bia_go_media_message ON bia_go.media_assets(message_id);
CREATE TABLE IF NOT EXISTS bia_go.conversation_runs (
 id text PRIMARY KEY, conversation_id text NOT NULL REFERENCES bia_go.conversations(id),
 version bigint NOT NULL, status text NOT NULL DEFAULT 'queued', agent text,
 response_id text, input_tokens bigint, created_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz
);
INSERT INTO bia_go.schema_versions(version) VALUES(2) ON CONFLICT DO NOTHING;
