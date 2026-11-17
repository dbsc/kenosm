package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"kenosm/internal/swarm"
)

// runState holds the event log of one swarm run and broadcasts new events to
// any connected SSE subscribers. It also carries the cancel function so a run
// can be stopped from the outside.
type runState struct {
	mu         sync.Mutex
	events     []swarm.Event
	subs       map[chan swarm.Event]struct{}
	done       bool
	stopped    bool
	cancel     context.CancelFunc
	finishedAt time.Time // when the run reached a terminal state (used for reaping)
}

func newRunState() *runState {
	return &runState{subs: make(map[chan swarm.Event]struct{})}
}

func (rs *runState) setCancel(c context.CancelFunc) {
	rs.mu.Lock()
	rs.cancel = c
	rs.mu.Unlock()
}

// stop cancels the run's context (if any) and marks it stopped. It returns
// false if the run was already finished.
func (rs *runState) stop() bool {
	rs.mu.Lock()
	if rs.done {
		rs.mu.Unlock()
		return false
	}
	rs.stopped = true
	c := rs.cancel
	rs.mu.Unlock()
	if c != nil {
		c()
	}
	return true
}

func (rs *runState) isStopped() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.stopped
}

func (rs *runState) add(e swarm.Event) {
	rs.mu.Lock()
	rs.events = append(rs.events, e)
	if e.Type == "done" || e.Type == "error" || e.Type == "stopped" {
		rs.done = true
		rs.finishedAt = time.Now()
	}
	subs := make([]chan swarm.Event, 0, len(rs.subs))
	for ch := range rs.subs {
		subs = append(subs, ch)
	}
	rs.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default: // slow subscriber; drop rather than block the run
		}
	}
}

// subscribe replays the events recorded so far and then streams future ones.
// Under the same lock it registers the subscriber, so no event is missed or
// duplicated. If the run is already finished the channel is closed after the
// replay (the terminal event is the last one in it).
func (rs *runState) subscribe() chan swarm.Event {
	rs.mu.Lock()
	// Size the channel to hold the full replay plus headroom for live events, so
	// replaying a long history never blocks while the lock is held. (A fixed
	// 128-buffer would deadlock: the 129th replayed event blocks, the lock never
	// releases, and every add() — and the run itself — stalls.)
	ch := make(chan swarm.Event, len(rs.events)+128)
	for _, e := range rs.events {
		ch <- e
	}
	if rs.done {
		close(ch)
	} else {
		rs.subs[ch] = struct{}{}
	}
	rs.mu.Unlock()
	return ch
}

func (rs *runState) unsubscribe(ch chan swarm.Event) {
	rs.mu.Lock()
	delete(rs.subs, ch)
	rs.mu.Unlock()
}

type runRegistry struct {
	mu   sync.Mutex
	runs map[string]*runState
}

func newRunRegistry() *runRegistry {
	return &runRegistry{runs: make(map[string]*runState)}
}

func (rr *runRegistry) create() (string, *runState) {
	// Opportunistically evict long-finished runs so their full event logs (and
	// final results) do not accumulate in memory forever. Each new run triggers
	// a sweep, so memory is bounded by runs created since the last one.
	rr.reapFinished(runReapMaxAge)

	id := randomID()
	rs := newRunState()
	rr.mu.Lock()
	rr.runs[id] = rs
	rr.mu.Unlock()
	return id, rs
}

// runReapMaxAge is how long a finished run's event log is kept after it ends so
// a late SSE subscriber can still replay it.
const runReapMaxAge = 10 * time.Minute

// reapFinished removes runs that finished more than maxAge ago. Active runs and
// recently finished runs are kept.
func (rr *runRegistry) reapFinished(maxAge time.Duration) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	now := time.Now()
	for id, rs := range rr.runs {
		rs.mu.Lock()
		done := rs.done
		finishedAt := rs.finishedAt
		rs.mu.Unlock()
		if done && !finishedAt.IsZero() && now.Sub(finishedAt) > maxAge {
			delete(rr.runs, id)
		}
	}
}

func (rr *runRegistry) get(id string) *runState {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return rr.runs[id]
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
