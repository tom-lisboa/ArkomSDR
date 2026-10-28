-- Version 3: classify old and new jobs by responsibility. A generated column
-- prevents producers from forgetting routing or choosing a different owner.
ALTER TABLE bia_go.jobs ADD COLUMN IF NOT EXISTS queue text GENERATED ALWAYS AS (
 CASE
  WHEN kind='event' AND payload->>'kind' IN ('booking.created','booking.rescheduled','booking.cancelled','attendance.attended','attendance.no_show','schedule.preference') THEN 'scheduling'
  WHEN kind='event' AND payload->>'kind'='cadence.resume' THEN 'followup'
  WHEN kind='effect' AND (payload->>'kind' IN ('book','cancel') OR (payload->>'kind'='send' AND payload->>'purpose'='booking_confirmation')) THEN 'scheduling'
  WHEN kind='effect' AND payload->>'kind'='send' AND payload->>'purpose' IN ('followup','reminder','recovery') THEN 'followup'
  ELSE 'execution'
 END
) STORED;
CREATE INDEX IF NOT EXISTS bia_go_jobs_queue_ready ON bia_go.jobs(queue,due_at,seq) WHERE status IN ('pending','running');
INSERT INTO bia_go.schema_versions(version) VALUES(3) ON CONFLICT DO NOTHING;
