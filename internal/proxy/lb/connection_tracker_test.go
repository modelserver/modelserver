package lb

import (
	"sync"
	"testing"
)

func TestConnectionTrackerAcquireReleaseCount(t *testing.T) {
	ct := NewConnectionTracker()

	// Initial count should be 0
	if got := ct.Count("upstream-1"); got != 0 {
		t.Errorf("initial Count = %d, want 0", got)
	}

	// Acquire increments
	ct.Acquire("upstream-1")
	if got := ct.Count("upstream-1"); got != 1 {
		t.Errorf("after Acquire Count = %d, want 1", got)
	}

	ct.Acquire("upstream-1")
	ct.Acquire("upstream-1")
	if got := ct.Count("upstream-1"); got != 3 {
		t.Errorf("after 3 Acquires Count = %d, want 3", got)
	}

	// Release decrements
	ct.Release("upstream-1")
	if got := ct.Count("upstream-1"); got != 2 {
		t.Errorf("after Release Count = %d, want 2", got)
	}

	ct.Release("upstream-1")
	ct.Release("upstream-1")
	if got := ct.Count("upstream-1"); got != 0 {
		t.Errorf("after all Releases Count = %d, want 0", got)
	}
}

func TestConnectionTrackerMultipleUpstreams(t *testing.T) {
	ct := NewConnectionTracker()

	ct.Acquire("A")
	ct.Acquire("A")
	ct.Acquire("B")

	if got := ct.Count("A"); got != 2 {
		t.Errorf("Count(A) = %d, want 2", got)
	}
	if got := ct.Count("B"); got != 1 {
		t.Errorf("Count(B) = %d, want 1", got)
	}
	if got := ct.Count("C"); got != 0 {
		t.Errorf("Count(C) = %d, want 0", got)
	}
}

func TestConnectionTrackerConcurrentAccess(t *testing.T) {
	ct := NewConnectionTracker()
	const goroutines = 100
	const opsPerGoroutine = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				ct.Acquire("shared")
			}
			for i := 0; i < opsPerGoroutine; i++ {
				ct.Release("shared")
			}
		}()
	}

	wg.Wait()

	// All goroutines acquired and released the same number of times
	if got := ct.Count("shared"); got != 0 {
		t.Errorf("after concurrent acquire/release Count = %d, want 0", got)
	}
}

func TestConnectionTrackerConcurrentMultipleUpstreams(t *testing.T) {
	ct := NewConnectionTracker()
	const goroutines = 50
	const ops = 500
	upstreams := []string{"alpha", "beta", "gamma"}

	var wg sync.WaitGroup
	wg.Add(goroutines * len(upstreams))

	for _, uid := range upstreams {
		for g := 0; g < goroutines; g++ {
			go func(id string) {
				defer wg.Done()
				for i := 0; i < ops; i++ {
					ct.Acquire(id)
				}
				for i := 0; i < ops; i++ {
					ct.Release(id)
				}
			}(uid)
		}
	}

	wg.Wait()

	for _, uid := range upstreams {
		if got := ct.Count(uid); got != 0 {
			t.Errorf("after concurrent ops Count(%s) = %d, want 0", uid, got)
		}
	}
}
