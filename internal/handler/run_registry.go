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
	frames     map[string]swarm.Event // latest browser_frame per agent (bounded)
	frameOrder []string               // first-seen agent order for frames
	subs       map[chan swarm.Event]struct{}
	done       bool
	stopped    bool
	exited     bool // the run's worker goroutine has returned
	cancel     context.CancelFunc
	finishedAt time.Time // when the run reached a terminal state (used for reaping)
}

func newRunState() *runState {
	return &runState{subs: make(map[chan swarm.Event]struct{}), frames: make(map[string]swarm.Event)}
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

// markExited records that the run's worker goroutine has returned. The event
// log may stay for late SSE subscribers; this flag is what tells callers the
// run is no longer doing anything.
func (rs *runState) markExited() {
	rs.mu.Lock()
	rs.exited = true
	rs.mu.Unlock()
}

func (rs *runState) add(e swarm.Event) {
	rs.mu.Lock()
	// Browser frames are high-volume (a full base64 PNG every tick, per agent).
	// Keep only the latest frame per agent instead of growing rs.events with
	// every frame; the frames are still broadcast live to subscribers below.
	if e.Type == "browser_frame" {
		if _, seen := rs.frames[e.Agent]; !seen {
			rs.frameOrder = append(rs.frameOrder, e.Agent)
		}
		rs.frames[e.Agent] = e
	} else {
		rs.events = append(rs.events, e)
	}
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

// browserFrames returns the latest browser_frame event per agent, in first-seen
// order — the initial state for the frontend's live browser grid.
func (rs *runState) browserFrames() []swarm.Event {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]swarm.Event, 0, len(rs.frameOrder))
	for _, a := range rs.frameOrder {
		out = append(out, rs.frames[a])
	}
	return out
}

type runRegistry struct {
	mu   sync.Mutex
	runs map[string]*runState
}

func newRunRegistry() *runRegistry {
	rr := &runRegistry{runs: make(map[string]*runState)}
	go rr.reapLoop()
	return rr
}

// reapLoop periodically reaps long-finished runs so their event logs don't sit
// in memory forever when no new run is created (create() also reaps on demand).
// The registry lives for the whole server process, so this is a process-lifetime
// daemon goroutine.
func (rr *runRegistry) reapLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		rr.reapFinished(runReapMaxAge)
	}
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

// latestWithFrames returns the most recently started run that has any
// browser_frame events — the frontend's browser pane uses this when the page
// was reloaded mid-run and the SSE history is gone.
func (rr *runRegistry) latestWithFrames() *runState {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	var best *runState
	var bestAt time.Time
	for _, rs := range rr.runs {
		rs.mu.Lock()
		has := len(rs.frameOrder) > 0
		var t time.Time
		if len(rs.events) > 0 {
			t = rs.events[0].Time // the run's "start" event
		}
		rs.mu.Unlock()
		if has && (best == nil || t.After(bestAt)) {
			best, bestAt = rs, t
		}
	}
	return best
}

// active counts runs whose worker goroutine has not returned yet.
func (rr *runRegistry) active() int {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	n := 0
	for _, rs := range rr.runs {
		rs.mu.Lock()
		if !rs.exited {
			n++
		}
		rs.mu.Unlock()
	}
	return n
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
