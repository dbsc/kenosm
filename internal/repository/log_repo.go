package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"kenosm/internal/model"
)

// LogRepo persists proxied request logs.
type LogRepo struct {
	pool *pgxpool.Pool
}

func NewLogRepo(pool *pgxpool.Pool) *LogRepo {
	return &LogRepo{pool: pool}
}

// InsertRequestLog implements the logging.Sink interface.
func (r *LogRepo) InsertRequestLog(ctx context.Context, e model.RequestLog) error {
	var sid any
	if e.ServiceID != "" {
		uid, err := uuidArg(e.ServiceID)
		if err != nil {
			return err
		}
		sid = uid
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO request_logs (service_id, request_id, method, path, status, latency_ms, error)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		sid, e.RequestID, e.Method, e.Path, e.Status, e.LatencyMS, e.Error,
	)
	return err
}

// Prune deletes request-log rows older than the cutoff and returns how many were
// removed, keeping the high-volume request_logs table bounded.
func (r *LogRepo) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM request_logs WHERE created_at < $1`, olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
