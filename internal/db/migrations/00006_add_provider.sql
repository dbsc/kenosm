-- +goose Up
ALTER TABLE services ADD COLUMN IF NOT EXISTS provider TEXT NOT NULL DEFAULT 'gemini';

-- +goose Down
ALTER TABLE services DROP COLUMN IF EXISTS provider;
