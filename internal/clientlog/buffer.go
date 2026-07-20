// Package clientlog provides an in-memory, bounded ring buffer that captures
// log output so it can be displayed inside the TUI instead of being written to
// stdout/stderr. The TUI runs on the alternate screen and renders to stderr;
// writing log lines there too would corrupt the display. Redirecting logrus to
// a Buffer keeps the terminal clean and lets the TUI show the same lines on a
// dedicated logs page.
//
// A Buffer implements io.Writer, so it can be handed to logrus.SetOutput
// (directly or via io.MultiWriter to also tee to a file). Because logrus can
// emit entries from any goroutine, all access is guarded by a mutex.
package clientlog

import (
	"strings"
	"sync"
)

// DefaultCapacity is the number of log lines a Buffer retains by default.
// Older lines are dropped once the buffer is full.
const DefaultCapacity = 500

// Buffer is a bounded, concurrency-safe ring of log lines. It implements
// io.Writer: each write is split into lines that are appended to the ring,
// dropping the oldest lines once capacity is exceeded.
type Buffer struct {
	mu       sync.Mutex
	lines    []string
	capacity int
	notify   func()
}

// NewBuffer returns a Buffer that retains up to capacity lines. A capacity of
// zero or less falls back to DefaultCapacity.
func NewBuffer(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Buffer{
		lines:    make([]string, 0, capacity),
		capacity: capacity,
	}
}

// Write implements io.Writer. The incoming bytes (one or more logrus entries)
// are split on newlines; each non-empty line is appended to the ring. Write
// never returns an error and always reports the full length as consumed, so it
// never disrupts the logger.
func (b *Buffer) Write(p []byte) (int, error) {
	text := strings.TrimRight(string(p), "\n")
	if text != "" {
		b.mu.Lock()
		for _, line := range strings.Split(text, "\n") {
			b.append(line)
		}
		notify := b.notify
		b.mu.Unlock()
		if notify != nil {
			notify()
		}
	}
	return len(p), nil
}

// append adds a single line to the ring, evicting the oldest line when the
// buffer is at capacity. The caller must hold b.mu.
func (b *Buffer) append(line string) {
	if len(b.lines) >= b.capacity {
		// Drop the oldest line. Copying keeps the slice from growing without
		// bound while retaining the newest capacity lines.
		copy(b.lines, b.lines[1:])
		b.lines[len(b.lines)-1] = line
		return
	}
	b.lines = append(b.lines, line)
}

// Lines returns a snapshot copy of the retained log lines, oldest first. The
// copy is safe to read without holding the lock.
func (b *Buffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

// Len returns the number of retained lines.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.lines)
}

// SetNotify registers a callback invoked (outside the lock) after each write
// that adds one or more lines. The TUI uses it to request a redraw. Passing
// nil clears the callback.
func (b *Buffer) SetNotify(fn func()) {
	b.mu.Lock()
	b.notify = fn
	b.mu.Unlock()
}
