CREATE TABLE torrent_settings (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    listen_port integer NOT NULL DEFAULT 51413 CHECK (listen_port BETWEEN 0 AND 65535),
    dht_enabled boolean NOT NULL DEFAULT true,
    pex_enabled boolean NOT NULL DEFAULT true,
    max_active_jobs integer NOT NULL DEFAULT 3 CHECK (max_active_jobs BETWEEN 1 AND 20),
    download_limit_kbps integer NOT NULL DEFAULT 0 CHECK (download_limit_kbps >= 0),
    upload_limit_kbps integer NOT NULL DEFAULT 0 CHECK (upload_limit_kbps >= 0),
    seed_ratio_limit double precision NOT NULL DEFAULT 1 CHECK (seed_ratio_limit >= 0),
    seed_time_limit_minutes integer NOT NULL DEFAULT 0 CHECK (seed_time_limit_minutes >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE torrent_jobs (
    id text PRIMARY KEY,
    info_hash text NOT NULL UNIQUE,
    name text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    release_id text NOT NULL DEFAULT '',
    source text NOT NULL DEFAULT 'magnet' CHECK (source IN ('magnet', 'file', 'torznab')),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'metadata', 'checking', 'downloading', 'seeding', 'paused', 'completed', 'failed')),
    magnet_uri text NOT NULL DEFAULT '',
    metainfo bytea,
    private boolean NOT NULL DEFAULT false,
    bytes_done bigint NOT NULL DEFAULT 0,
    uploaded bigint NOT NULL DEFAULT 0,
    bytes_total bigint NOT NULL DEFAULT 0,
    pieces_total integer NOT NULL DEFAULT 0,
    pieces_done integer NOT NULL DEFAULT 0,
    piece_bits bytea NOT NULL DEFAULT '',
    files jsonb NOT NULL DEFAULT '[]',
    seed_ratio_limit double precision NOT NULL DEFAULT 1 CHECK (seed_ratio_limit >= 0),
    seed_time_limit_minutes integer NOT NULL DEFAULT 0 CHECK (seed_time_limit_minutes >= 0),
    seeding_started_at timestamptz,
    seeding_seconds bigint NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT '',
    added_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE INDEX torrent_jobs_status_idx ON torrent_jobs (status, added_at);

CREATE TABLE torrent_sources (
    id text PRIMARY KEY,
    name text NOT NULL,
    url text NOT NULL,
    api_key text NOT NULL DEFAULT '',
    categories jsonb NOT NULL DEFAULT '[]',
    enabled boolean NOT NULL DEFAULT true,
    added_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
