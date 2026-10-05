ALTER TABLE downloads DROP CONSTRAINT IF EXISTS downloads_media_type_check;
ALTER TABLE downloads ADD CONSTRAINT downloads_media_type_check
    CHECK (media_type IN ('', 'movie', 'tv', 'music'));

CREATE TABLE music_artists (
    id text PRIMARY KEY,
    musicbrainz_id text,
    name text NOT NULL,
    data jsonb NOT NULL,
    added_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX music_artists_musicbrainz_key ON music_artists (musicbrainz_id) WHERE musicbrainz_id IS NOT NULL;
CREATE INDEX music_artists_name_idx ON music_artists (lower(name));
CREATE INDEX music_artists_updated_idx ON music_artists (updated_at DESC);

CREATE TABLE music_albums (
    id text PRIMARY KEY,
    artist_id text NOT NULL REFERENCES music_artists(id) ON DELETE CASCADE,
    musicbrainz_id text,
    title text NOT NULL,
    release_date text NOT NULL DEFAULT '',
    album_type text NOT NULL DEFAULT '',
    data jsonb NOT NULL,
    added_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX music_albums_musicbrainz_key ON music_albums (musicbrainz_id) WHERE musicbrainz_id IS NOT NULL;
CREATE INDEX music_albums_artist_idx ON music_albums (artist_id);
CREATE INDEX music_albums_updated_idx ON music_albums (updated_at DESC);

CREATE TABLE music_tracks (
    id text PRIMARY KEY,
    album_id text NOT NULL REFERENCES music_albums(id) ON DELETE CASCADE,
    disc integer NOT NULL DEFAULT 1,
    number integer NOT NULL DEFAULT 0,
    title text NOT NULL DEFAULT '',
    data jsonb NOT NULL,
    UNIQUE (album_id, disc, number)
);

CREATE INDEX music_tracks_album_idx ON music_tracks (album_id, disc, number);

CREATE TABLE music_config (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    data jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE music_acquisitions (
    job_id text PRIMARY KEY REFERENCES downloads(id) ON DELETE CASCADE,
    album_id text NOT NULL REFERENCES music_albums(id) ON DELETE CASCADE,
    release jsonb NOT NULL DEFAULT '{}',
    status text NOT NULL DEFAULT '',
    error text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX music_acquisitions_album_idx ON music_acquisitions (album_id);

CREATE TABLE music_history (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    album_id text NOT NULL DEFAULT '',
    artist_id text NOT NULL DEFAULT '',
    type text NOT NULL,
    message text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX music_history_album_idx ON music_history (album_id, id DESC);

CREATE TABLE music_blocklist (
    album_id text NOT NULL REFERENCES music_albums(id) ON DELETE CASCADE,
    release_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (album_id, release_id)
);

CREATE TABLE music_automation (
    name text PRIMARY KEY,
    last_run timestamptz NOT NULL DEFAULT now()
);

-- Music claims never take a download another media type already owns.
CREATE FUNCTION music_claim_download() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owned boolean;
BEGIN
    SELECT movie_adopted OR media_type IN ('movie', 'tv') INTO owned FROM downloads WHERE id = NEW.job_id FOR UPDATE;
    IF owned THEN
        RAISE EXCEPTION 'download % already belongs to another media type', NEW.job_id USING ERRCODE = '23505';
    END IF;
    UPDATE downloads SET movie_adopted = true, media_type = 'music' WHERE id = NEW.job_id;
    RETURN NEW;
END;
$$;

CREATE TRIGGER music_acquisition_claims_download AFTER INSERT ON music_acquisitions
FOR EACH ROW EXECUTE FUNCTION music_claim_download();

-- Movie and TV adoption must not claim a music download.
CREATE FUNCTION music_download_protected() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM downloads WHERE id = NEW.job_id AND media_type = 'music') THEN
        RAISE EXCEPTION 'download % belongs to music', NEW.job_id USING ERRCODE = '23505';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER movie_adoption_skips_music BEFORE INSERT ON movie_acquisitions
FOR EACH ROW EXECUTE FUNCTION music_download_protected();

CREATE TRIGGER tv_adoption_skips_music BEFORE INSERT ON tv_acquisitions
FOR EACH ROW EXECUTE FUNCTION music_download_protected();
