-- Version 12: SMS uses the follow-up queue and its own delivery ledger.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM bia_go.schema_versions WHERE version=12) THEN
  ALTER TABLE bia_go.jobs DROP COLUMN queue;
  ALTER TABLE bia_go.jobs ADD COLUMN queue text GENERATED ALWAYS AS (
   CASE
    WHEN kind='event' AND payload->>'kind' IN ('booking.created','booking.rescheduled','booking.cancelled','attendance.attended','attendance.no_show','schedule.preference') THEN 'scheduling'
    WHEN kind='event' AND payload->>'kind'='cadence.resume' THEN 'followup'
    WHEN kind='effect' AND payload->>'kind'='send_audio' THEN 'media'
    WHEN kind='effect' AND payload->>'purpose' IN ('meeting_audio','meeting_call','meeting_sms') THEN 'followup'
    WHEN kind='effect' AND (payload->>'kind' IN ('book','cancel') OR (payload->>'kind'='send' AND payload->>'purpose'='booking_confirmation')) THEN 'scheduling'
    WHEN kind='effect' AND payload->>'kind'='send' AND payload->>'purpose' IN ('followup','reminder','recovery') THEN 'followup'
    ELSE 'execution'
   END
  ) STORED;
  CREATE INDEX bia_go_jobs_queue_ready ON bia_go.jobs(queue,due_at,seq) WHERE status IN ('pending','running');
  INSERT INTO bia_go.schema_versions(version) VALUES(12);
 END IF;
END $$;
CREATE TABLE IF NOT EXISTS bia_go.sms_messages (
 id text PRIMARY KEY,
 phone text NOT NULL,
 fingerprint text NOT NULL,
 sid text UNIQUE,
 status text NOT NULL DEFAULT 'initiating',
 error_code text,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
