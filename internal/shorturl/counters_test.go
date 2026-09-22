package shorturl

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// newCounters returns a batcher over a fake store with a settable clock.
func newCounters() (*counters, *fakeStore, *time.Time) {
	fs := &fakeStore{}
	now := time.Date(2026, 9, 21, 20, 0, 0, 0, time.UTC)
	c := &counters{
		store:    fs,
		logger:   quiet,
		interval: 5 * time.Second,
		timeout:  time.Second,
		now:      func() time.Time { return now },
	}
	return c, fs, &now
}

func TestCountersBatchPerInterval(t *testing.T) {
	c, fs, now := newCounters()
	a := CounterDoc{Host: "example.com", Slug: "a"}
	b := CounterDoc{Host: "example.com", Slug: "b"}
	for range 3 {
		c.add(a, counterClick)
	}
	c.add(b, counterClick, counterQRUse)

	ctx := context.Background()
	c.start(ctx)()
	if fs.adds != 2 || fs.count("example.com/a", "click") != 3 || fs.count("example.com/b", "qrUse") != 1 {
		t.Fatalf("first flush: adds=%d counts=%v", fs.adds, fs.counts)
	}

	*now = now.Add(time.Second)
	c.add(a, counterClick)
	c.start(ctx)()
	if fs.adds != 2 {
		t.Errorf("flushed %d times before the interval passed, want 2", fs.adds)
	}
	visit := *now
	*now = now.Add(5 * time.Second)
	c.start(ctx)()
	if fs.adds != 3 || fs.count("example.com/a", "click") != 4 {
		t.Errorf("second flush: adds=%d counts=%v", fs.adds, fs.counts)
	}
	if got := fs.last("example.com/a", "click"); !got.Equal(visit) {
		t.Errorf("clickLast = %v, want the visit time %v, not the flush time", got, visit)
	}
}

func TestCountersRequeueUnappliedWrite(t *testing.T) {
	c, fs, now := newCounters()
	a := CounterDoc{Host: "example.com", Slug: "a"}
	fs.addErr = func(CounterDoc) error { return fmt.Errorf("contention: %w", ErrCounterRetry) }
	c.add(a, counterClick)
	c.add(a, counterClick)
	first := *now
	c.start(context.Background())()
	if fs.count("example.com/a", "click") != 0 {
		t.Fatalf("failed write stored counts: %v", fs.counts)
	}

	fs.addErr = nil
	*now = now.Add(5 * time.Second)
	c.add(a, counterClick)
	c.start(context.Background())()
	if got := fs.count("example.com/a", "click"); got != 3 {
		t.Errorf("click count after retry = %d, want 3", got)
	}
	if got := fs.last("example.com/a", "click"); !got.After(first) {
		t.Errorf("clickLast = %v, want the later visit", got)
	}
}

func TestCountersDropAmbiguousFailure(t *testing.T) {
	for name, err := range map[string]error{
		"timeout": context.DeadlineExceeded,
		"deleted": errors.New("rpc error: code = NotFound"),
	} {
		t.Run(name, func(t *testing.T) {
			c, fs, now := newCounters()
			fs.addErr = func(CounterDoc) error { return err }
			c.add(CounterDoc{Host: "example.com", Slug: "a"}, counterClick)
			c.start(context.Background())()
			fs.addErr = nil
			*now = now.Add(time.Minute)
			c.start(context.Background())()
			if fs.adds != 1 {
				t.Errorf("AddCounts called %d times, want 1: the failed write must not be retried", fs.adds)
			}
		})
	}
}

func TestCountersFinalFlushDoesNotRequeue(t *testing.T) {
	c, fs, _ := newCounters()
	fs.addErr = func(CounterDoc) error { return ErrCounterRetry }
	c.add(CounterDoc{Host: "example.com", Slug: "a"}, counterClick)
	c.flush(context.Background())
	if c.take(false) != nil {
		t.Error("the final flush left counts pending, which nothing will write")
	}
}

// blockingStore holds AddCounts until released, to observe a flush in
// progress.
type blockingStore struct {
	fakeStore
	entered chan struct{}
	release chan struct{}
}

func (b *blockingStore) AddCounts(ctx context.Context, doc CounterDoc, deltas map[string]Delta) error {
	b.entered <- struct{}{}
	<-b.release
	return b.fakeStore.AddCounts(ctx, doc, deltas)
}

func TestCountersOneFlushAtATime(t *testing.T) {
	bs := &blockingStore{entered: make(chan struct{}, 2), release: make(chan struct{})}
	now := time.Date(2026, 9, 21, 20, 0, 0, 0, time.UTC)
	c := &counters{store: bs, logger: quiet, interval: time.Nanosecond, timeout: time.Second, now: func() time.Time { return now }}
	c.add(CounterDoc{Host: "example.com", Slug: "a"}, counterClick)
	wait := c.start(context.Background())
	<-bs.entered

	// While that write is blocked, a second request neither blocks nor
	// starts another flush.
	c.add(CounterDoc{Host: "example.com", Slug: "a"}, counterClick)
	now = now.Add(time.Second)
	c.start(context.Background())()

	close(bs.release)
	wait()
	if got := bs.count("example.com/a", "click"); got != 1 {
		t.Errorf("stored %d clicks during the blocked flush, want 1", got)
	}
	c.flush(context.Background())
	if got := bs.count("example.com/a", "click"); got != 2 {
		t.Errorf("stored %d clicks after the final flush, want 2", got)
	}
}

// TestCountersConcurrentRequests sends many requests at once, with a flush
// due on nearly every one, and checks that no count is lost or doubled.
func TestCountersConcurrentRequests(t *testing.T) {
	fs := &fakeStore{links: map[string]Link{"example.com/demo": {Destination: "https://example.org/"}}}
	h := &Handler{Store: fs, Logger: quiet, CounterInterval: time.Nanosecond}
	const n = 500
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			r := httptest.NewRequestWithContext(t.Context(), "GET", "http://example.com/demo", nil)
			h.ServeHTTP(httptest.NewRecorder(), r)
		})
	}
	wg.Wait()
	h.FlushCounters(context.Background())
	if got := fs.count("example.com/demo", "click"); got != n {
		t.Errorf("click count = %d, want %d", got, n)
	}
}
