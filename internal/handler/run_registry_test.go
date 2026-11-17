package handler

import (
	"testing"
	"time"

	"kenosm/internal/swarm"
)

// TestSubscribeReplayLargeHistory guards against the deadlock where subscribe()
// replayed a long history into a fixed 128-buffer channel while holding the
// runState mutex. It must return promptly even for hundreds of events.
func TestSubscribeReplayLargeHistory(t *testing.T) {
	rs := newRunState()
	const n = 500
	for i := 0; i < n; i++ {
		rs.add(swarm.Event{Type: "tool", Step: i})
	}

	type result struct{ ch chan swarm.Event }
	got := make(chan result, 1)
	go func() { got <- result{rs.subscribe()} }()

	var ch chan swarm.Event
	select {
	case r := <-got:
		ch = r.ch
	case <-time.After(2 * time.Second):
		t.Fatalf("subscribe blocked replaying a %d-event history (deadlock)", n)
	}
	defer rs.unsubscribe(ch)

	for i := 0; i < n; i++ {
		select {
		case e := <-ch:
			if e.Step != i {
				t.Fatalf("replayed event %d has step %d, want %d", i, e.Step, i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out reading replayed event %d/%d", i, n)
		}
	}
}

// TestSubscribeReplayClosedWhenDone verifies a finished run's replay is
// delivered and then the channel is closed so the SSE handler can return.
func TestSubscribeReplayClosedWhenDone(t *testing.T) {
	rs := newRunState()
	rs.add(swarm.Event{Type: "start"})
	rs.add(swarm.Event{Type: "done"})

	ch := rs.subscribe()
	events := 0
	for range ch {
		events++
	}
	if events != 2 {
		t.Fatalf("replayed %d events, want 2", events)
	}
}

func TestReapFinished(t *testing.T) {
	rr := newRunRegistry()

	idOld, rsOld := rr.create()
	rsOld.mu.Lock()
	rsOld.done = true
	rsOld.finishedAt = time.Now().Add(-time.Hour)
	rsOld.mu.Unlock()

	idNew, _ := rr.create() // active: done == false

	rr.reapFinished(10 * time.Minute)

	if rr.get(idOld) != nil {
		t.Fatalf("finished run older than max age should be reaped")
	}
	if rr.get(idNew) == nil {
		t.Fatalf("active run should not be reaped")
	}
}

func TestReapFinishedKeepsRecent(t *testing.T) {
	rr := newRunRegistry()
	id, rs := rr.create()
	rs.mu.Lock()
	rs.done = true
	rs.finishedAt = time.Now() // just finished
	rs.mu.Unlock()

	rr.reapFinished(10 * time.Minute)

	if rr.get(id) == nil {
		t.Fatalf("recently finished run should be kept for late SSE replay")
	}
}
