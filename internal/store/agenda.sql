-- Version 7: private request/reply mailbox between voice and scheduling workers.
ALTER TABLE bia_go.voice_requests ADD COLUMN IF NOT EXISTS booking_id text;
ALTER TABLE bia_go.voice_requests ADD COLUMN IF NOT EXISTS booking_start timestamptz;
ALTER TABLE bia_go.bookings ADD COLUMN IF NOT EXISTS briefing jsonb NOT NULL DEFAULT '{}';
CREATE TABLE IF NOT EXISTS bia_go.agenda_requests (
 id text PRIMARY KEY,
 voice_request_id text NOT NULL REFERENCES bia_go.voice_requests(id),
 kind text NOT NULL CHECK(kind IN ('lookup','note')),
 body text NOT NULL DEFAULT '',
 result jsonb,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL DEFAULT now()+interval '15 seconds',
 completed_at timestamptz
);
CREATE INDEX IF NOT EXISTS bia_go_agenda_pending ON bia_go.agenda_requests(created_at) WHERE completed_at IS NULL;
INSERT INTO bia_go.schema_versions(version) VALUES(7) ON CONFLICT DO NOTHING;
