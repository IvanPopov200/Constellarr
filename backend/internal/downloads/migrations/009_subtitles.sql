CREATE TABLE subtitle_config (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    data jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE subtitle_profiles (
    id text PRIMARY KEY,
    data jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE subtitle_assignments (
    video_kind text NOT NULL CHECK (video_kind IN ('movie', 'episode')),
    video_id text NOT NULL,
    profile_id text NOT NULL DEFAULT '',
    monitored boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (video_kind, video_id)
);

CREATE TABLE subtitle_sidecars (
    video_kind text NOT NULL,
    video_id text NOT NULL,
    rel_path text NOT NULL,
    root_id text NOT NULL DEFAULT '',
    root_path text NOT NULL DEFAULT '',
    language text NOT NULL,
    format text NOT NULL,
    forced boolean NOT NULL DEFAULT false,
    hi boolean NOT NULL DEFAULT false,
    source text NOT NULL DEFAULT 'scan',
    size bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (video_kind, video_id, rel_path)
);

CREATE INDEX subtitle_sidecars_path_idx ON subtitle_sidecars (root_path, rel_path);

CREATE TABLE subtitle_wanted (
    video_kind text NOT NULL,
    video_id text NOT NULL,
    language text NOT NULL,
    forced boolean NOT NULL DEFAULT false,
    hi boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'wanted',
    attempts integer NOT NULL DEFAULT 0,
    last_attempt_at timestamptz,
    next_attempt_at timestamptz,
    error text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (video_kind, video_id, language, forced, hi)
);

CREATE INDEX subtitle_wanted_due_idx ON subtitle_wanted (next_attempt_at) WHERE status = 'wanted';

CREATE TABLE subtitle_history (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    video_kind text NOT NULL DEFAULT '',
    video_id text NOT NULL DEFAULT '',
    action text NOT NULL,
    language text NOT NULL DEFAULT '',
    message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX subtitle_history_video_idx ON subtitle_history (video_kind, video_id, created_at DESC);

CREATE TABLE subtitle_jobs (
    id text PRIMARY KEY,
    video_kind text NOT NULL DEFAULT '',
    video_id text NOT NULL DEFAULT '',
    kind text NOT NULL,
    language text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'queued',
    progress integer NOT NULL DEFAULT 0,
    detail text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX subtitle_jobs_video_idx ON subtitle_jobs (video_kind, video_id, created_at DESC);

CREATE INDEX subtitle_jobs_open_idx ON subtitle_jobs (created_at) WHERE status IN ('queued', 'running');

CREATE TABLE subtitle_outputs (
    id text PRIMARY KEY,
    job_id text NOT NULL DEFAULT '',
    video_kind text NOT NULL,
    video_id text NOT NULL,
    language text NOT NULL DEFAULT '',
    forced boolean NOT NULL DEFAULT false,
    hi boolean NOT NULL DEFAULT false,
    format text NOT NULL DEFAULT 'srt',
    origin_path text NOT NULL DEFAULT '',
    target_path text NOT NULL DEFAULT '',
    payload text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    detail text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    applied_at timestamptz
);

CREATE INDEX subtitle_outputs_video_idx ON subtitle_outputs (video_kind, video_id, status);

CREATE TABLE subtitle_failures (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    video_kind text NOT NULL DEFAULT '',
    video_id text NOT NULL DEFAULT '',
    language text NOT NULL DEFAULT '',
    provider text NOT NULL DEFAULT '',
    message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX subtitle_failures_video_idx ON subtitle_failures (video_kind, video_id, created_at DESC);
