package nodereport

import (
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestLimiterWindow(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	l := MustNewLimiter(2, time.Minute, 10, clk.now)
	for i, want := range []bool{true, true, false} {
		if got := l.Allow("k"); got != want {
			t.Errorf("call %d: got %v, want %v", i, got, want)
		}
	}
	clk.advance(time.Minute + time.Second)
	if !l.Allow("k") {
		t.Error("want allow after the window passed")
	}
}

func TestLimiterCapacityEvictsLRU(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	l := MustNewLimiter(1, time.Hour, 3, clk.now)
	for _, k := range []string{"a", "b", "c", "d"} {
		if !l.Allow(k) {
			t.Errorf("first %s denied", k)
			continue
		}
		if l.Len() > 3 {
			t.Errorf("Len = %d after %s, exceeds capacity", l.Len(), k)
		}
	}
	if l.Len() != 3 {
		t.Fatalf("Len = %d, want 3", l.Len())
	}
	if !l.Allow("a") {
		t.Error("a was evicted, so a fresh a gets a full budget")
	}
	if l.Allow("d") {
		t.Error("d is retained and already spent its budget")
	}
}

func TestLimiterConcurrent(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	l := MustNewLimiter(5, time.Minute, 8, clk.now)
	var wg sync.WaitGroup
	for g := range 50 {
		wg.Go(func() {
			for i := range 100 {
				l.Allow(string(rune('a' + (g+i)%20)))
				if l.Len() > 8 {
					t.Error("Len exceeded capacity")
					return
				}
			}
		})
	}
	wg.Wait()
	if l.Len() > 8 {
		t.Errorf("Len = %d, want <= 8", l.Len())
	}
}

func TestMustNewLimiterPanicsOnBadBounds(t *testing.T) {
	for _, tt := range []struct {
		name            string
		limit, capacity int
	}{
		{"zero capacity", 1, 0}, {"negative capacity", 1, -1}, {"zero limit", 0, 1}, {"negative limit", -1, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("want panic")
				}
			}()
			MustNewLimiter(tt.limit, time.Minute, tt.capacity, time.Now)
		})
	}
}
