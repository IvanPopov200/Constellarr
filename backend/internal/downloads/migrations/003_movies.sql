CREATE TABLE movies (
    id text PRIMARY KEY,
    imdb_id text,
    data jsonb NOT NULL,
    added_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX movies_imdb_id_key ON movies (imdb_id) WHERE imdb_id IS NOT NULL;
CREATE INDEX movies_profile_idx ON movies ((data->>'profileId'));
CREATE INDEX movies_updated_idx ON movies (updated_at DESC);

CREATE TABLE movie_profiles (
    id text PRIMARY KEY,
    name text NOT NULL UNIQUE,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE movie_config (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    data jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE movie_acquisitions (
    job_id text PRIMARY KEY REFERENCES downloads(id) ON DELETE CASCADE,
    movie_id text NOT NULL REFERENCES movies(id) ON DELETE CASCADE,
    release jsonb NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX movie_acquisitions_movie_idx ON movie_acquisitions (movie_id);

CREATE TABLE movie_history (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    movie_id text NOT NULL REFERENCES movies(id) ON DELETE CASCADE,
    type text NOT NULL,
    message text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX movie_history_movie_idx ON movie_history (movie_id, id DESC);

CREATE TABLE movie_blocklist (
    movie_id text NOT NULL REFERENCES movies(id) ON DELETE CASCADE,
    release_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (movie_id, release_id)
);

CREATE TABLE movie_watchlists (
    id text PRIMARY KEY,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX movie_watchlists_profile_idx ON movie_watchlists ((data->>'profileId'));
CREATE INDEX movie_watchlists_updated_idx ON movie_watchlists (updated_at DESC);
