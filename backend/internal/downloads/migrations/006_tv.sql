ALTER TABLE downloads ADD COLUMN media_type text NOT NULL DEFAULT ''
    CHECK (media_type IN ('', 'movie', 'tv'));

UPDATE downloads SET media_type = 'movie'
WHERE id IN (SELECT job_id FROM movie_acquisitions);

CREATE OR REPLACE FUNCTION mark_movie_download_adopted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE downloads SET movie_adopted = true, media_type = 'movie' WHERE id = NEW.job_id;
    RETURN NEW;
END;
$$;

CREATE TABLE tv_series (
    id text PRIMARY KEY,
    imdb_id text,
    profile_id text REFERENCES movie_profiles(id) ON DELETE RESTRICT,
    data jsonb NOT NULL,
    added_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX tv_series_imdb_id_key ON tv_series (imdb_id) WHERE imdb_id IS NOT NULL;
CREATE INDEX tv_series_profile_idx ON tv_series (profile_id);
CREATE INDEX tv_series_updated_idx ON tv_series (updated_at DESC);

CREATE TABLE tv_episodes (
    id text PRIMARY KEY,
    series_id text NOT NULL REFERENCES tv_series(id) ON DELETE CASCADE,
    season integer NOT NULL,
    number integer NOT NULL,
    data jsonb NOT NULL,
    UNIQUE (series_id, season, number)
);

CREATE TABLE tv_config (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    data jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tv_acquisitions (
    job_id text PRIMARY KEY REFERENCES downloads(id) ON DELETE CASCADE,
    series_id text NOT NULL REFERENCES tv_series(id) ON DELETE CASCADE,
    release jsonb NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tv_acquisitions_series_idx ON tv_acquisitions (series_id);

CREATE TABLE tv_history (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    series_id text NOT NULL REFERENCES tv_series(id) ON DELETE CASCADE,
    episode_id text NOT NULL DEFAULT '',
    type text NOT NULL,
    message text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tv_history_series_idx ON tv_history (series_id, id DESC);

CREATE TABLE tv_blocklist (
    series_id text NOT NULL REFERENCES tv_series(id) ON DELETE CASCADE,
    release_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (series_id, release_id)
);

CREATE TABLE tv_automation (
    name text PRIMARY KEY,
    last_run timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION mark_tv_download_adopted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE downloads SET movie_adopted = true, media_type = 'tv' WHERE id = NEW.job_id;
    RETURN NEW;
END;
$$;

CREATE TRIGGER tv_download_adopted AFTER INSERT ON tv_acquisitions
FOR EACH ROW EXECUTE FUNCTION mark_tv_download_adopted();
