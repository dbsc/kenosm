package swarm

import (
	"time"

	"kenosm/internal/llm"
)

// Event is a progress event emitted while a swarm run executes. The handler
// streams these to the frontend over Server-Sent Events so a user can watch
// the agents work in real time.
type Event struct {
	Time   time.Time `json:"time"`
	Type   string    `json:"type"` // planning, plan, subtask, tool, subtask_done, usage, done, error
	Agent  string    `json:"agent,omitempty"`
	Model  string    `json:"model,omitempty"`
	Detail string    `json:"detail,omitempty"`
	Usage  llm.Usage `json:"usage,omitempty"`
	Result *Result   `json:"result,omitempty"`
	Error  string    `json:"error,omitempty"`
}

// Emitter receives progress events. It may be nil (events are then dropped).
type Emitter func(Event)
