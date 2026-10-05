CREATE TABLE operations_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT now(),
    kind text NOT NULL CHECK (kind IN ('download_failed', 'import_error', 'provider_failure', 'backup', 'backup_failed', 'restore', 'alert', 'notification_failed')),
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    source text NOT NULL DEFAULT '',
    message text NOT NULL DEFAULT '',
    ref text NOT NULL DEFAULT ''
);

CREATE INDEX operations_events_recent_idx ON operations_events (id DESC);
CREATE INDEX operations_events_kind_idx ON operations_events (kind, id DESC);

CREATE TABLE operations_alert_rules (
    name text PRIMARY KEY,
    enabled boolean NOT NULL DEFAULT true,
    severity text NOT NULL DEFAULT 'warning' CHECK (severity IN ('info', 'warning', 'critical')),
    config jsonb NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO operations_alert_rules (name, severity, config) VALUES
    ('filesystem_low_space', 'warning', '{"minimumFreeBytes": 10737418240, "minimumFreePercent": 10}'),
    ('provider_failures', 'warning', '{"windowMinutes": 60, "threshold": 3}'),
    ('download_import_failures', 'warning', '{"windowMinutes": 60, "threshold": 3}'),
    ('job_stuck', 'warning', '{"stuckMinutes": 120}'),
    ('db_health', 'critical', '{"failures": 2}');

CREATE TABLE operations_alert_state (
    name text PRIMARY KEY REFERENCES operations_alert_rules(name) ON DELETE CASCADE,
    firing boolean NOT NULL DEFAULT false,
    severity text NOT NULL DEFAULT 'warning',
    message text NOT NULL DEFAULT '',
    value double precision NOT NULL DEFAULT 0,
    since timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE operations_alert_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL,
    firing boolean NOT NULL,
    severity text NOT NULL,
    message text NOT NULL DEFAULT '',
    value double precision NOT NULL DEFAULT 0,
    notified_at timestamptz,
    at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX operations_alert_events_recent_idx ON operations_alert_events (id DESC);
CREATE INDEX operations_alert_events_notify_idx ON operations_alert_events (name, firing, notified_at DESC);

CREATE TABLE operations_config (
    name text PRIMARY KEY,
    data jsonb NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO operations_config (name, data) VALUES ('webhook', '{}'), ('backups', '{}');
