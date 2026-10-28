-- Version 9: durable meeting cadence and outbound media ownership.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM bia_go.schema_versions WHERE version=9) THEN
  ALTER TABLE bia_go.jobs DROP COLUMN queue;
  ALTER TABLE bia_go.jobs ADD COLUMN queue text GENERATED ALWAYS AS (
   CASE
    WHEN kind='event' AND payload->>'kind' IN ('booking.created','booking.rescheduled','booking.cancelled','attendance.attended','attendance.no_show','schedule.preference') THEN 'scheduling'
    WHEN kind='event' AND payload->>'kind'='cadence.resume' THEN 'followup'
    WHEN kind='effect' AND payload->>'kind'='send_audio' THEN 'media'
    WHEN kind='effect' AND payload->>'purpose' IN ('meeting_audio','meeting_call') THEN 'followup'
    WHEN kind='effect' AND (payload->>'kind' IN ('book','cancel') OR (payload->>'kind'='send' AND payload->>'purpose'='booking_confirmation')) THEN 'scheduling'
    WHEN kind='effect' AND payload->>'kind'='send' AND payload->>'purpose' IN ('followup','reminder','recovery') THEN 'followup'
    ELSE 'execution'
   END
  ) STORED;
  CREATE INDEX bia_go_jobs_queue_ready ON bia_go.jobs(queue,due_at,seq) WHERE status IN ('pending','running');
  ALTER TABLE bia_go.voice_requests ADD COLUMN scheduled_reminder boolean NOT NULL DEFAULT false;
  INSERT INTO bia_go.schema_versions(version) VALUES(9);
 END IF;
END $$;
