package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ActivityEntry is one recorded action an agent took during a run: a read or
// write tool call, the start/end of a subtask, or a model's text output.
type ActivityEntry struct {
	ID        int64     `json:"id"`
	RunID     string    `json:"run_id"`
	Agent     string    `json:"agent"`
	Model     string    `json:"model"`
	Direction string    `json:"direction"` // read | write | task | task_done | text | other
	Tool      string    `json:"tool"`
	Detail    string    `json:"detail"`
	Step      int       `json:"step"`
	Wave      int       `json:"wave"`
	Time      time.Time `json:"time"`
}

// TimelineBar is one subtask on the parallel-distribution timeline: which agent
// ran it, when it started and finished, and what it produced.
type TimelineBar struct {
	Agent     string     `json:"agent"`
	Model     string     `json:"model"`
	Step      int        `json:"step"`
	Wave      int        `json:"wave"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Summary   string     `json:"summary,omitempty"`
}

// ActivityRepo persists the per-agent activity log.
type ActivityRepo struct {
	pool *pgxpool.Pool
}

func NewActivityRepo(pool *pgxpool.Pool) *ActivityRepo {
	return &ActivityRepo{pool: pool}
}

const activityColumns = "id, run_id, agent, model, direction, tool, detail, step, wave, created_at"

func scanActivity(row interface{ Scan(...any) error }) (ActivityEntry, error) {
	var a ActivityEntry
	if err := row.Scan(&a.ID, &a.RunID, &a.Agent, &a.Model, &a.Direction, &a.Tool, &a.Detail, &a.Step, &a.Wave, &a.Time); err != nil {
		return ActivityEntry{}, err
	}
	return a, nil
}

// Insert records one activity entry.
func (r *ActivityRepo) Insert(ctx context.Context, a ActivityEntry) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO activity (run_id, agent, model, direction, tool, detail, step, wave)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		a.RunID, a.Agent, a.Model, a.Direction, a.Tool, a.Detail, a.Step, a.Wave,
	)
	return err
}

// Recent returns the latest activity across all runs, newest first.
func (r *ActivityRepo) Recent(ctx context.Context, limit int) ([]ActivityEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+activityColumns+` FROM activity ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectActivity(rows)
}

// ByRun returns the activity for one run, oldest first (natural timeline order).
func (r *ActivityRepo) ByRun(ctx context.Context, runID string, limit int) ([]ActivityEntry, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+activityColumns+` FROM activity WHERE run_id = $1 ORDER BY created_at ASC, id ASC LIMIT $2`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectActivity(rows)
}

// Timeline reconstructs the parallel-distribution timeline for one run: for each
// subtask it pairs the "task" (start) entry with its "task_done" (end) entry, so
// callers can draw per-agent bars spanning the subtask's execution time.
func (r *ActivityRepo) Timeline(ctx context.Context, runID string) ([]TimelineBar, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT agent, model, direction, detail, step, wave, created_at
		 FROM activity
		 WHERE run_id = $1 AND direction IN ('task','task_done')
		 ORDER BY created_at ASC, id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type key struct {
		agent string
		step  int
	}
	started := map[key]TimelineBar{}
	var order []key
	for rows.Next() {
		var agent, model, direction, detail string
		var step, wave int
		var ts time.Time
		if err := rows.Scan(&agent, &model, &direction, &detail, &step, &wave, &ts); err != nil {
			return nil, err
		}
		k := key{agent, step}
		if direction == "task" {
			started[k] = TimelineBar{Agent: agent, Model: model, Step: step, Wave: wave, StartedAt: ts}
			order = append(order, k)
			continue
		}
		// task_done: attach the end time and summary to the matching start.
		if b, ok := started[k]; ok {
			b.EndedAt = &ts
			b.Summary = detail
			started[k] = b
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	bars := make([]TimelineBar, 0, len(order))
	for _, k := range order {
		bars = append(bars, started[k])
	}
	return bars, nil
}

// Prune deletes activity rows older than the cutoff and returns how many were
// removed, keeping the activity table bounded.
func (r *ActivityRepo) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM activity WHERE created_at < $1`, olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func collectActivity(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]ActivityEntry, error) {
	out := make([]ActivityEntry, 0)
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
