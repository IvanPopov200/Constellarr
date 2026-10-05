CREATE TABLE discovery_requests (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    media_type text NOT NULL CHECK (media_type IN ('movie', 'tv', 'music')),
    provider text NOT NULL DEFAULT '',
    provider_id text NOT NULL CHECK (length(provider_id) BETWEEN 1 AND 128),
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 256),
    year integer NOT NULL DEFAULT 0 CHECK (year BETWEEN 0 AND 2200),
    poster text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approving', 'approved', 'rejected', 'cancelled', 'available')),
    message text NOT NULL DEFAULT '',
    decision_note text NOT NULL DEFAULT '',
    decided_by text NOT NULL DEFAULT '',
    decided_at timestamptz,
    library_id text NOT NULL DEFAULT '',
    delivery jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT discovery_requests_approved_needs_library
        CHECK (status NOT IN ('approved', 'available') OR library_id <> '')
);

-- Only rejected and cancelled requests release the key so a user can submit the media again.
CREATE UNIQUE INDEX discovery_requests_active_key
    ON discovery_requests (user_id, media_type, provider_id)
    WHERE status IN ('pending', 'approving', 'approved', 'available');

CREATE INDEX discovery_requests_user_idx ON discovery_requests (user_id, created_at DESC);
CREATE INDEX discovery_requests_queue_idx ON discovery_requests (status, created_at DESC);

CREATE TABLE discovery_request_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id text NOT NULL REFERENCES discovery_requests(id) ON DELETE CASCADE,
    actor text NOT NULL,
    action text NOT NULL,
    from_status text NOT NULL DEFAULT '',
    to_status text NOT NULL DEFAULT '',
    message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX discovery_request_events_request_idx ON discovery_request_events (request_id, id);

CREATE TABLE discovery_request_comments (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id text NOT NULL REFERENCES discovery_requests(id) ON DELETE CASCADE,
    user_id text NOT NULL,
    body text NOT NULL CHECK (length(body) BETWEEN 1 AND 1000),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX discovery_request_comments_request_idx ON discovery_request_comments (request_id, id);

CREATE TABLE discovery_ai_config (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    data jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discovery_recommendations (
    id text PRIMARY KEY,
    user_id text NOT NULL,
    model text NOT NULL DEFAULT '',
    media_type text NOT NULL DEFAULT '',
    input jsonb NOT NULL DEFAULT '{}',
    candidates jsonb NOT NULL DEFAULT '[]',
    warnings jsonb NOT NULL DEFAULT '[]',
    accepted_at timestamptz,
    accepted_action text NOT NULL DEFAULT '',
    accepted_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX discovery_recommendations_user_idx ON discovery_recommendations (user_id, created_at DESC);

CREATE TABLE discovery_automation (
    name text PRIMARY KEY,
    last_run timestamptz NOT NULL DEFAULT now()
);
