package logging

import (
	"context"
	"log/slog"

	"kenosm/internal/model"
)

// Sink persists request logs. Implemented by repository.LogRepo.
type Sink interface {
	InsertRequestLog(ctx context.Context, e model.RequestLog) error
}

// Logger buffers request log entries and writes them asynchronously so the
// proxied response path never blocks on a database insert.
type Logger struct {
	ch   chan model.RequestLog
	sink Sink
	log  *slog.Logger
}

func New(ctx context.Context, sink Sink, log *slog.Logger, buffer int) *Logger {
	l := &Logger{
		ch:   make(chan model.RequestLog, buffer),
		sink: sink,
		log:  log,
	}
	go l.worker(ctx)
	return l
}

// Log enqueues an entry, dropping it if the buffer is full.
func (l *Logger) Log(e model.RequestLog) {
	select {
	case l.ch <- e:
	default:
		l.log.Warn("request log buffer full, dropping entry")
	}
}

func (l *Logger) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-l.ch:
			if err := l.sink.InsertRequestLog(ctx, e); err != nil {
				l.log.Error("insert request log", "err", err)
			}
		}
	}
}
