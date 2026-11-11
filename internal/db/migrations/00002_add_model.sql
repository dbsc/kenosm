-- +goose Up
ALTER TABLE services ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE services DROP COLUMN IF EXISTS model;
