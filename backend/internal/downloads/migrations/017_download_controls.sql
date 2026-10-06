ALTER TABLE downloads ADD COLUMN pause_reason text NOT NULL DEFAULT '';

ALTER TABLE downloads DROP CONSTRAINT downloads_status_check;
ALTER TABLE downloads ADD CONSTRAINT downloads_status_check
    CHECK (status IN ('queued', 'downloading', 'verifying', 'repairing', 'extracting', 'paused', 'cancelled', 'completed', 'failed'));
