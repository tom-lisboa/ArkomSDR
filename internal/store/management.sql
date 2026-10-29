-- Management is a projection and an operator control plane over the Go SDR.
CREATE TABLE IF NOT EXISTS bia_go.management_users (
 id text PRIMARY KEY, username text UNIQUE NOT NULL, name text NOT NULL,
 role text NOT NULL CHECK(role IN ('viewer','operator','admin')),
 password_hash text NOT NULL, enabled boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS bia_go.management_sessions (
 token_hash text PRIMARY KEY, user_id text NOT NULL REFERENCES bia_go.management_users(id),
 expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS management_sessions_user ON bia_go.management_sessions(user_id);
CREATE TABLE IF NOT EXISTS bia_go.management_login_limits (
 id text PRIMARY KEY, attempts integer NOT NULL, reset_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS bia_go.lead_management (
 lead_id text PRIMARY KEY REFERENCES bia_go.leads(id),
 status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','closed','won','lost')),
 assigned_to text REFERENCES bia_go.management_users(id),
 first_won_at timestamptz, closed_at timestamptz, updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS bia_go.management_actions (
 id text PRIMARY KEY, actor_id text NOT NULL REFERENCES bia_go.management_users(id),
 lead_id text REFERENCES bia_go.leads(id), kind text NOT NULL,
 fingerprint text NOT NULL, request jsonb NOT NULL, result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 seq bigint GENERATED ALWAYS AS IDENTITY UNIQUE
);
CREATE INDEX IF NOT EXISTS management_actions_lead ON bia_go.management_actions(lead_id,seq DESC);
CREATE TABLE IF NOT EXISTS bia_go.meeting_attendance (
 booking_id text PRIMARY KEY, lead_id text NOT NULL REFERENCES bia_go.leads(id),
 starts_at timestamptz NOT NULL, ends_at timestamptz NOT NULL,
 outcome text NOT NULL CHECK(outcome IN ('attended','no_show')),
 actor_id text REFERENCES bia_go.management_users(id),
 source text NOT NULL DEFAULT 'operator',
 recorded_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS meeting_attendance_lead ON bia_go.meeting_attendance(lead_id,starts_at DESC);
CREATE INDEX IF NOT EXISTS management_leads_updated ON bia_go.leads(updated_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS management_jobs_lead_kind ON bia_go.jobs(lead_id,kind,seq DESC);
CREATE INDEX IF NOT EXISTS management_bookings_created ON bia_go.bookings(created_at,lead_id);
INSERT INTO bia_go.schema_versions(version) VALUES(17) ON CONFLICT DO NOTHING;
-- Operator interruption also stops ringing calls through the existing voice worker.
ALTER TABLE bia_go.voice_requests ADD COLUMN IF NOT EXISTS stop_state text NOT NULL DEFAULT '';
ALTER TABLE bia_go.voice_requests ADD COLUMN IF NOT EXISTS stop_lease_until timestamptz;
CREATE INDEX IF NOT EXISTS bia_go_voice_operator_stop ON bia_go.voice_requests(updated_at,id) WHERE stop_state='pending';
