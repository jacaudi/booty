package nodereport

import (
	"container/list"
	"fmt"
	"sync"
	"time"
)

// Limiter is a fixed-window rate limiter keyed by string, with a hard cap
// on the number of keys it remembers: the least recently used key is
// evicted first, so pre-auth traffic with spoofed keys cannot grow it.
type Limiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	capacity int
	now      func() time.Time
	order    *list.List // front = most recently used; values are *limitEntry
	entries  map[string]*list.Element
}

type limitEntry struct {
	key   string
	start time.Time
	count int
}

// MustNewLimiter is NewLimiter for callers that pass constants: it panics
// where NewLimiter returns an error.
func MustNewLimiter(limit int, window time.Duration, capacity int, now func() time.Time) *Limiter {
	l, err := NewLimiter(limit, window, capacity, now)
	if err != nil {
		panic(err)
	}
	return l
}

// NewLimiter allows limit events per key per window and remembers at most
// capacity keys. It returns an error unless both are at least 1. now supplies
// the clock.
func NewLimiter(limit int, window time.Duration, capacity int, now func() time.Time) (*Limiter, error) {
	if limit < 1 || capacity < 1 {
		return nil, fmt.Errorf("nodereport: a limiter needs limit >= 1 and capacity >= 1, got %d and %d", limit, capacity)
	}
	return &Limiter{
		max:      limit,
		window:   window,
		capacity: capacity,
		now:      now,
		order:    list.New(),
		entries:  make(map[string]*list.Element),
	}, nil
}

// Allow records an event for key and reports whether it is within budget.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if el, ok := l.entries[key]; ok {
		l.order.MoveToFront(el)
		e, _ := el.Value.(*limitEntry)
		if now.Sub(e.start) >= l.window {
			e.start, e.count = now, 0
		}
		if e.count >= l.max {
			return false
		}
		e.count++
		return true
	}
	l.entries[key] = l.order.PushFront(&limitEntry{key: key, start: now, count: 1})
	for l.order.Len() > l.capacity {
		oldest := l.order.Back()
		l.order.Remove(oldest)
		if e, ok := oldest.Value.(*limitEntry); ok {
			delete(l.entries, e.key)
		}
	}
	return l.max > 0
}

// Len returns the number of keys currently remembered.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}
