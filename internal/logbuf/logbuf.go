package logbuf

import (
	"sync"
	"time"

	"flexconnect/internal/types"
)

type Buffer struct {
	mu      sync.Mutex
	entries []types.LogEntry
	limit   int
}

func New(limit int) *Buffer {
	if limit <= 0 {
		limit = 200
	}
	return &Buffer{limit: limit}
}

func (b *Buffer) Add(level, message string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = append(b.entries, types.LogEntry{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Level:   level,
		Message: message,
	})
	if len(b.entries) > b.limit {
		b.entries = append([]types.LogEntry(nil), b.entries[len(b.entries)-b.limit:]...)
	}
}

func (b *Buffer) Snapshot() []types.LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]types.LogEntry(nil), b.entries...)
}
