-- +goose Up
-- Cost of a usage row, in USD (computed at insert time from the model's price).
ALTER TABLE usage ADD COLUMN IF NOT EXISTS cost NUMERIC(16,8) NOT NULL DEFAULT 0;

-- Activity log: what each agent reads/writes and the subtask boundaries, so the
-- dashboard can show a live feed and a parallel distribution timeline.
CREATE TABLE IF NOT EXISTS activity (
    id         BIGSERIAL PRIMARY KEY,
    run_id     TEXT NOT NULL DEFAULT '',
    agent      TEXT NOT NULL DEFAULT '',
    model      TEXT NOT NULL DEFAULT '',
    direction  TEXT NOT NULL DEFAULT '', -- read | write | task | task_done | text | other
    tool       TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT '',
    step       INTEGER NOT NULL DEFAULT 0,
    wave       INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_activity_run ON activity (run_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_activity_created_at ON activity (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS activity;
ALTER TABLE usage DROP COLUMN IF EXISTS cost;
