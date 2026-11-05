-- Internal SDR only. A connected Google account is never automatically enabled.
CREATE SEQUENCE IF NOT EXISTS bia_go.seller_closer_seq START 1000000;
CREATE TABLE IF NOT EXISTS bia_go.seller_calendars (
 user_id text PRIMARY KEY REFERENCES bia_go.management_users(id) ON DELETE CASCADE,
 closer_id bigint NOT NULL UNIQUE DEFAULT nextval('bia_go.seller_closer_seq'),
 enabled boolean NOT NULL DEFAULT false,
 account_id text REFERENCES bia_go.google_accounts(id) ON DELETE SET NULL,
 calendar_id text NOT NULL DEFAULT '',
 calendar_name text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS bia_go.seller_calendar_blocks (
 user_id text NOT NULL REFERENCES bia_go.seller_calendars(user_id) ON DELETE CASCADE,
 account_id text NOT NULL REFERENCES bia_go.google_accounts(id) ON DELETE CASCADE,
 calendar_id text NOT NULL,
 PRIMARY KEY(user_id,account_id,calendar_id)
);
-- Intent is committed before any Google mutation. Pending intents hold capacity
-- even after a process crash; only verified Google results confirm a booking.
CREATE TABLE IF NOT EXISTS bia_go.google_booking_ops (
 operation_id text PRIMARY KEY,
 kind text NOT NULL CHECK(kind IN ('book','cancel')),
 lead_id text NOT NULL REFERENCES bia_go.leads(id),
 slot_id text NOT NULL REFERENCES bia_go.slots(id),
 booking_id text NOT NULL,
 old_booking_id text NOT NULL DEFAULT '',
 user_id text NOT NULL,
 account_id text NOT NULL,
 calendar_id text NOT NULL,
 event_id text NOT NULL,
 payload jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','confirmed','failed')),
 error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS google_booking_pending ON bia_go.google_booking_ops(slot_id) WHERE status='pending';
CREATE UNIQUE INDEX IF NOT EXISTS google_booking_binding ON bia_go.google_booking_ops(booking_id) WHERE kind='book' AND status='confirmed';
CREATE UNIQUE INDEX IF NOT EXISTS google_booking_one_intent ON bia_go.google_booking_ops(lead_id) WHERE status='pending';
INSERT INTO bia_go.schema_versions(version) VALUES(22) ON CONFLICT DO NOTHING;
