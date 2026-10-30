-- Version 19: both proactive cadences belong to the existing follow-up process.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM bia_go.schema_versions WHERE version=19) THEN
  ALTER TABLE bia_go.jobs DROP COLUMN queue;
  ALTER TABLE bia_go.jobs ADD COLUMN queue text GENERATED ALWAYS AS (
   CASE
    WHEN kind='event' AND payload->>'kind' IN ('booking.created','booking.rescheduled','booking.cancelled','attendance.attended','attendance.no_show','schedule.preference') THEN 'scheduling'
    WHEN kind='event' AND payload->>'kind'='cadence.resume' THEN 'followup'
    WHEN kind='effect' AND payload->>'kind' IN ('send_audio','send_video') THEN 'media'
    WHEN kind='effect' AND (payload->>'purpose' LIKE 'no_reply_%' OR payload->>'purpose' LIKE 'meeting_checkin_%' OR payload->>'purpose' IN ('meeting_audio','meeting_call','meeting_sms','meeting_preparation','meeting_start')) THEN 'followup'
    WHEN kind='effect' AND (payload->>'kind' IN ('book','cancel') OR (payload->>'kind'='send' AND payload->>'purpose'='booking_confirmation')) THEN 'scheduling'
    WHEN kind='effect' AND payload->>'kind'='send' AND payload->>'purpose' IN ('followup','reminder','recovery') THEN 'followup'
    ELSE 'execution'
   END
  ) STORED;
  CREATE INDEX bia_go_jobs_queue_ready ON bia_go.jobs(queue,due_at,seq) WHERE status IN ('pending','running');
  INSERT INTO bia_go.schema_versions(version) VALUES(19);
 END IF;
END $$;
