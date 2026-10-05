CREATE TABLE IF NOT EXISTS migration_plans (
    id text PRIMARY KEY,
    data jsonb NOT NULL,
    status text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS migration_plan_items (
    plan_id text NOT NULL REFERENCES migration_plans (id) ON DELETE CASCADE,
    position integer NOT NULL,
    kind text NOT NULL,
    target text NOT NULL,
    label text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    message text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (plan_id, position)
);

CREATE INDEX IF NOT EXISTS migration_plans_expires_idx ON migration_plans (expires_at);
