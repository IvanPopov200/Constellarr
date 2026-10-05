CREATE TABLE torrent_processing (
    job_id text PRIMARY KEY REFERENCES torrent_jobs(id) ON DELETE CASCADE,
    state text NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'completed', 'failed', 'skipped')),
    error text NOT NULL DEFAULT '',
    input_root text NOT NULL DEFAULT '',
    files jsonb NOT NULL DEFAULT '[]',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX torrent_processing_state_idx ON torrent_processing (state, updated_at);
