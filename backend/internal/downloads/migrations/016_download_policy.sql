CREATE TABLE download_policy (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    config jsonb NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO download_policy (id) VALUES (true) ON CONFLICT (id) DO NOTHING;
