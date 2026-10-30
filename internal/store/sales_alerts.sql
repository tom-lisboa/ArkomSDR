-- Seller notifications use their own ledger, never the client outreach queue.
CREATE TABLE IF NOT EXISTS bia_go.seller_whatsapp (
 user_id text PRIMARY KEY REFERENCES bia_go.management_users(id),
 instance_id text UNIQUE, instance_name text NOT NULL, credential bytea,
 status text NOT NULL DEFAULT 'provisioning', phone text NOT NULL DEFAULT '',
 notifications_enabled boolean NOT NULL DEFAULT true,
 checked_at timestamptz, last_error text NOT NULL DEFAULT '',
 generation bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS bia_go.sales_progress (
 lead_id text PRIMARY KEY REFERENCES bia_go.leads(id),
 stage text NOT NULL, activity_at timestamptz NOT NULL, since timestamptz NOT NULL,
 eligible boolean NOT NULL, episode bigint NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS bia_go.sales_notifications (
 id text PRIMARY KEY, lead_id text NOT NULL REFERENCES bia_go.leads(id),
 user_id text NOT NULL REFERENCES bia_go.management_users(id),
 episode bigint NOT NULL, generation bigint NOT NULL,
 stage text NOT NULL, stalled_since timestamptz NOT NULL, activity_at timestamptz NOT NULL,
 status text NOT NULL CHECK(status IN ('pending','sending','accepted','sent','failed','uncertain','skipped','simulated')),
 mode text NOT NULL CHECK(mode IN ('live','shadow')),
 provider_id text NOT NULL DEFAULT '', error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 UNIQUE(lead_id,episode,user_id)
);
CREATE INDEX IF NOT EXISTS sales_notifications_ready ON bia_go.sales_notifications(status,updated_at);
INSERT INTO bia_go.schema_versions(version) VALUES(18) ON CONFLICT DO NOTHING;
