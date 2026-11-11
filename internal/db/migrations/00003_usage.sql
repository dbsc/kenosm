-- +goose Up
CREATE TABLE IF NOT EXISTS usage (
    id                BIGSERIAL PRIMARY KEY,
    run_id            TEXT NOT NULL DEFAULT '',
    task              TEXT NOT NULL DEFAULT '',
    agent             TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    thought_tokens    INTEGER NOT NULL DEFAULT 0,
    total_tokens      INTEGER NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_usage_created_at ON usage (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_usage_model ON usage (model);

-- +goose Down
DROP TABLE IF EXISTS usage;
