ALTER TABLE torrent_jobs DROP CONSTRAINT torrent_jobs_status_check;
ALTER TABLE torrent_jobs ADD CONSTRAINT torrent_jobs_status_check
    CHECK (status IN ('queued', 'metadata', 'checking', 'downloading', 'seeding', 'paused', 'completed', 'failed', 'cancelled'));
