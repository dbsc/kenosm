-- +goose Up
CREATE TABLE IF NOT EXISTS services (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug             TEXT NOT NULL UNIQUE,
    name             TEXT NOT NULL DEFAULT '',
    upstream_url     TEXT NOT NULL,
    upstream_api_key TEXT NOT NULL DEFAULT '',
    auth_header      TEXT NOT NULL DEFAULT 'Authorization',
    auth_scheme      TEXT NOT NULL DEFAULT 'Bearer',
    proxy_host       TEXT NOT NULL DEFAULT '',
    proxy_port       INTEGER NOT NULL DEFAULT 0,
    proxy_username   TEXT NOT NULL DEFAULT '',
    proxy_password   TEXT NOT NULL DEFAULT '',
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_services_slug ON services (slug);

CREATE TABLE IF NOT EXISTS request_logs (
    id          BIGSERIAL PRIMARY KEY,
    service_id  UUID REFERENCES services(id) ON DELETE SET NULL,
    request_id  TEXT NOT NULL DEFAULT '',
    method      TEXT NOT NULL,
    path        TEXT NOT NULL,
    status      INTEGER,
    latency_ms  INTEGER,
    error       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_request_logs_created_at ON request_logs (created_at);

-- +goose Down
DROP TABLE IF EXISTS request_logs;
DROP TABLE IF EXISTS services;
