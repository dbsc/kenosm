package logging

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"kenosm/internal/model"
)

// Sink persists request logs. Implemented by repository.LogRepo.
type Sink interface {
	InsertRequestLog(ctx context.Context, e model.RequestLog) error
}

// Logger buffers request log entries and writes them asynchronously so the
// proxied response path never blocks on a database insert. Close drains the
// remaining buffer and must be called on shutdown so no entries are lost.
type Logger struct {
	mu     sync.Mutex
	ch     chan model.RequestLog
	sink   Sink
	log    *slog.Logger
	closed bool
	done   chan struct{}
}

func New(sink Sink, log *slog.Logger, buffer int) *Logger {
	l := &Logger{
		ch:   make(chan model.RequestLog, buffer),
		sink: sink,
		log:  log,
		done: make(chan struct{}),
	}
	go l.worker()
	return l
}

// Log enqueues an entry, dropping it if the buffer is full. It is safe to call
// from many goroutines; after Close it becomes a no-op.
func (l *Logger) Log(e model.RequestLog) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	select {
	case l.ch <- e:
	default:
		l.log.Warn("request log buffer full, dropping entry")
	}
}

// Close stops accepting entries, drains the buffered ones, and blocks until the
// worker has flushed them all. It is idempotent and safe to call concurrently
// with Log.
func (l *Logger) Close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	close(l.ch)
	l.mu.Unlock()
	<-l.done
}

func (l *Logger) worker() {
	defer close(l.done)
	for e := range l.ch {
		l.insert(e)
	}
}

// insert writes one entry with its own timeout, independent of the server's
// lifecycle context, so the final drain on shutdown still reaches the database.
func (l *Logger) insert(e model.RequestLog) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.sink.InsertRequestLog(ctx, e); err != nil {
		l.log.Error("insert request log", "err", err)
	}
}
