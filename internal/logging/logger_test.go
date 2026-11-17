package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"kenosm/internal/model"
)

type fakeSink struct {
	mu  sync.Mutex
	got []model.RequestLog
}

func (f *fakeSink) InsertRequestLog(_ context.Context, e model.RequestLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, e)
	return nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func TestLoggerDrainsOnClose(t *testing.T) {
	sink := &fakeSink{}
	l := New(sink, slog.New(slog.NewTextHandler(io.Discard, nil)), 64)

	const n = 50
	for i := 0; i < n; i++ {
		l.Log(model.RequestLog{RequestID: fmt.Sprintf("r%d", i)})
	}
	l.Close()
	l.Close() // idempotent

	if got := sink.count(); got != n {
		t.Fatalf("drained %d entries, want %d", got, n)
	}

	// Log after close is a no-op.
	l.Log(model.RequestLog{RequestID: "after"})
	if got := sink.count(); got != n {
		t.Fatalf("log after close recorded an entry: got %d, want %d", got, n)
	}
}
