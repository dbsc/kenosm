package handler

import "testing"

func TestRunSemaphore(t *testing.T) {
	h := &Swarm{sem: make(chan struct{}, 2)}

	if !h.tryAcquireRun() || !h.tryAcquireRun() {
		t.Fatal("expected two acquisitions to succeed at capacity 2")
	}
	if h.tryAcquireRun() {
		t.Fatal("third acquisition should fail at capacity 2")
	}

	h.releaseRun()
	if !h.tryAcquireRun() {
		t.Fatal("expected acquisition to succeed after a release")
	}
}
