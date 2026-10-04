CREATE TABLE download_library_files (
    job_id text NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,
    name text NOT NULL,
    root_path text NOT NULL DEFAULT '',
    path text NOT NULL DEFAULT '',
    size bigint NOT NULL DEFAULT 0,
    sha256 text NOT NULL DEFAULT '',
    ready boolean NOT NULL DEFAULT false,
    PRIMARY KEY (job_id, name)
);

CREATE INDEX download_library_files_path_idx ON download_library_files (path) WHERE ready;
