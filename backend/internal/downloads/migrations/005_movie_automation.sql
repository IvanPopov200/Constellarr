CREATE TABLE movie_automation (
    name text PRIMARY KEY,
    last_run timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE downloads ADD COLUMN movie_adopted boolean NOT NULL DEFAULT false;

CREATE FUNCTION mark_movie_download_adopted() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE downloads SET movie_adopted = true WHERE id = NEW.job_id;
    RETURN NEW;
END;
$$;

CREATE TRIGGER movie_download_adopted AFTER INSERT ON movie_acquisitions
FOR EACH ROW EXECUTE FUNCTION mark_movie_download_adopted();
