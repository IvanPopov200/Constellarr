CREATE TABLE downloads (
    id text PRIMARY KEY,
    release_id text NOT NULL UNIQUE,
    title text NOT NULL,
    nzb bytea NOT NULL,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'downloading', 'verifying', 'repairing', 'extracting', 'completed', 'failed')),
    bytes_done bigint NOT NULL DEFAULT 0,
    bytes_total bigint NOT NULL DEFAULT 0,
    segments_done integer NOT NULL DEFAULT 0,
    segments_total integer NOT NULL DEFAULT 0,
    missing_segments integer NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT '',
    files jsonb NOT NULL DEFAULT '[]',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
