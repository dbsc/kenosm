package repository

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// UsageRepo persists and aggregates token usage across swarm runs.
type UsageRepo struct {
	pool *pgxpool.Pool
}

func NewUsageRepo(pool *pgxpool.Pool) *UsageRepo {
	return &UsageRepo{pool: pool}
}

// UsageEntry is one recorded usage line: an agent (API key) + model used for a
// given run/task, plus the USD cost of that call.
type UsageEntry struct {
	RunID            string
	Task             string
	Agent            string
	Model            string
	PromptTokens     int
	CompletionTokens int
	ThoughtTokens    int
	TotalTokens      int
	Cost             float64
}

// Insert persists one usage entry.
func (r *UsageRepo) Insert(ctx context.Context, u UsageEntry) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO usage (run_id, task, agent, model, prompt_tokens, completion_tokens, thought_tokens, total_tokens, cost)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		u.RunID, u.Task, u.Agent, u.Model, u.PromptTokens, u.CompletionTokens, u.ThoughtTokens, u.TotalTokens, u.Cost,
	)
	return err
}

// Stats is the aggregated token usage for the frontend panel.
type Stats struct {
	Totals  Totals     `json:"totals"`
	ByModel []GroupRow `json:"by_model"`
	ByAgent []GroupRow `json:"by_agent"`
	ByRun   []RunRow   `json:"by_run"`
}

type Totals struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	ThoughtTokens    int     `json:"thought_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float64 `json:"cost"`
	Calls            int     `json:"calls"`
	Runs             int     `json:"runs"`
}

type GroupRow struct {
	Key              string  `json:"key"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Cost             float64 `json:"cost"`
	Calls            int     `json:"calls"`
}

type RunRow struct {
	RunID       string  `json:"run_id"`
	Task        string  `json:"task"`
	TotalTokens int     `json:"total_tokens"`
	Cost        float64 `json:"cost"`
	Calls       int     `json:"calls"`
}

// Stats returns the current aggregated usage.
func (r *UsageRepo) Stats(ctx context.Context) (Stats, error) {
	var s Stats

	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
		        COALESCE(SUM(thought_tokens),0), COALESCE(SUM(total_tokens),0),
		        COALESCE(SUM(cost),0), COUNT(*), COUNT(DISTINCT run_id)
		 FROM usage`,
	).Scan(&s.Totals.PromptTokens, &s.Totals.CompletionTokens, &s.Totals.ThoughtTokens,
		&s.Totals.TotalTokens, &s.Totals.Cost, &s.Totals.Calls, &s.Totals.Runs)
	if err != nil {
		return s, err
	}

	if s.ByModel, err = r.groupBy(ctx, "model"); err != nil {
		return s, err
	}
	if s.ByAgent, err = r.groupBy(ctx, "agent"); err != nil {
		return s, err
	}

	rows, err := r.pool.Query(ctx,
		`SELECT run_id, MAX(task), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost),0), COUNT(*)
		 FROM usage GROUP BY run_id
		 ORDER BY MAX(created_at) DESC LIMIT 20`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	s.ByRun = make([]RunRow, 0)
	for rows.Next() {
		var rr RunRow
		if err := rows.Scan(&rr.RunID, &rr.Task, &rr.TotalTokens, &rr.Cost, &rr.Calls); err != nil {
			return s, err
		}
		s.ByRun = append(s.ByRun, rr)
	}
	if err := rows.Err(); err != nil {
		return s, err
	}

	return s, nil
}

// groupBy aggregates usage by a fixed column ("model" or "agent"). The column
// is supplied only by internal callers, never user input.
func (r *UsageRepo) groupBy(ctx context.Context, column string) ([]GroupRow, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+column+` AS key, COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost),0), COUNT(*)
		 FROM usage GROUP BY `+column+` ORDER BY SUM(total_tokens) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]GroupRow, 0)
	for rows.Next() {
		var g GroupRow
		if err := rows.Scan(&g.Key, &g.PromptTokens, &g.CompletionTokens, &g.TotalTokens, &g.Cost, &g.Calls); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Prune deletes usage rows older than the cutoff and returns how many were
// removed, keeping the high-volume usage table bounded.
func (r *UsageRepo) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM usage WHERE created_at < $1`, olderThan)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
