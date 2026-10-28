-- Isolated schema: never modifies the n8n or funil_aplicacao tables.
CREATE SCHEMA IF NOT EXISTS bia_go;
CREATE TABLE IF NOT EXISTS bia_go.leads (
    id text PRIMARY KEY,
    state jsonb NOT NULL,
    revision bigint NOT NULL DEFAULT 0,
    lease_token text,
    lease_until timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS bia_go.jobs (
    id text PRIMARY KEY,
    seq bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    lead_id text NOT NULL REFERENCES bia_go.leads(id),
    kind text NOT NULL CHECK (kind IN ('event','effect')),
    mode text NOT NULL CHECK (mode IN ('shadow','live')),
    payload jsonb NOT NULL,
    fingerprint text NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','running','done','simulated','skipped','dead','uncertain')),
    attempts integer NOT NULL DEFAULT 0,
    due_at timestamptz NOT NULL,
    lease_token text,
    result jsonb,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
CREATE INDEX IF NOT EXISTS bia_go_jobs_ready ON bia_go.jobs(due_at, seq)
    WHERE status IN ('pending','running');
CREATE INDEX IF NOT EXISTS bia_go_jobs_lead ON bia_go.jobs(lead_id, seq);
CREATE INDEX IF NOT EXISTS bia_go_jobs_completed ON bia_go.jobs(completed_at, lead_id)
    WHERE status = 'done' AND kind = 'effect';
CREATE TABLE IF NOT EXISTS bia_go.schema_versions(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
INSERT INTO bia_go.schema_versions(version) VALUES (1) ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS bia_go.crm_notes (
    operation_id text PRIMARY KEY,
    lead_id text NOT NULL REFERENCES bia_go.leads(id),
    purpose text NOT NULL,
    body text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS bia_go.slots (
    id text PRIMARY KEY,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL CHECK(ends_at>starts_at),
    closer_id bigint NOT NULL CHECK(closer_id>0),
    meeting_url text NOT NULL,
    reserved_by text,
    UNIQUE(closer_id, starts_at)
);
CREATE INDEX IF NOT EXISTS bia_go_slots_available ON bia_go.slots(starts_at,closer_id) WHERE reserved_by IS NULL;
CREATE TABLE IF NOT EXISTS bia_go.bookings (
    id text PRIMARY KEY,
    operation_id text NOT NULL UNIQUE,
    lead_id text NOT NULL REFERENCES bia_go.leads(id),
    slot_id text NOT NULL REFERENCES bia_go.slots(id),
    payload jsonb NOT NULL,
    cancelled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS bia_go_booking_active_lead ON bia_go.bookings(lead_id) WHERE cancelled_at IS NULL;
