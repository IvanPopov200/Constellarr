ALTER TABLE downloads ADD COLUMN protocol text NOT NULL DEFAULT 'usenet'
    CHECK (protocol IN ('usenet', 'torrent'));
CREATE INDEX downloads_queue_protocol_idx ON downloads (protocol, status, created_at);
