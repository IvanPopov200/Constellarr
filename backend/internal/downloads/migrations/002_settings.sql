CREATE TABLE settings (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    indexer_url text NOT NULL,
    indexer_api_key text NOT NULL DEFAULT '',
    usenet_host text NOT NULL,
    usenet_port integer NOT NULL CHECK (usenet_port BETWEEN 1 AND 65535),
    usenet_username text NOT NULL DEFAULT '',
    usenet_password text NOT NULL DEFAULT '',
    usenet_connections integer NOT NULL CHECK (usenet_connections BETWEEN 1 AND 32),
    usenet_fallback_hosts jsonb NOT NULL DEFAULT '[]',
    updated_at timestamptz NOT NULL DEFAULT now()
);
