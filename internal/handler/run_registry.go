package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"kenosm/internal/swarm"
)

// runState holds the event log of one swarm run and broadcasts new events to
// any connected SSE subscribers. It also carries the cancel function so a run
// can be stopped from the outside.
type runState struct {
	mu      sync.Mutex
	events  []swarm.Event
	subs    map[chan swarm.Event]struct{}
	done    bool
	stopped bool
	cancel  context.CancelFunc
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
	ch := make(chan swarm.Event, 128)
	rs.mu.Lock()
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
	id := randomID()
	rs := newRunState()
	rr.mu.Lock()
	rr.runs[id] = rs
	rr.mu.Unlock()
	return id, rs
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
