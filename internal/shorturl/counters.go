package shorturl

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// CounterDoc identifies the document that holds a set of counters: a link
// document, or one of its path rules when Rule is set.
type CounterDoc struct {
	Host, Slug, Rule string
}

// String returns the document's path below the database root. The Firestore
// store resolves the document from this path, so it is the one place the
// layout is spelled out.
func (d CounterDoc) String() string {
	if d.Rule != "" {
		return d.Host + "/" + d.Slug + "/paths/" + d.Rule
	}
	return d.Host + "/" + d.Slug
}

// Delta is a pending change to one counter: the number of visits to add and
// the time of the latest one.
type Delta struct {
	N    int64
	Last time.Time
}

// ErrCounterRetry marks an AddCounts failure that the database reports was
// not applied, so writing the same counts again cannot double count.
var ErrCounterRetry = errors.New("counter write was not applied")

// Counter names. Each one maps to a "<name>Count" and "<name>Last" field pair
// on the counter document.
const (
	counterClick    = "click"
	counterQRUse    = "qrUse"
	counterQRCreate = "qrCreate"
	counterMatch    = "match"
)

const (
	defaultCounterInterval = 5 * time.Second
	defaultCounterTimeout  = time.Second
)

// counters batches analytics increments in memory so a busy link costs one
// Firestore write per flush interval per instance instead of one per request.
// Firestore sustains about one write per second on a single document, and
// per-request writes to a busy passthrough link timed out or were aborted
// for contention.
//
// A flush runs alongside a request and finishes before that request does,
// because Cloud Run's request-based billing throttles CPU between requests.
// An idle instance therefore holds its counts until its next request or its
// shutdown.
type counters struct {
	store    Store
	logger   *slog.Logger
	interval time.Duration
	timeout  time.Duration
	now      func() time.Time

	mu      sync.Mutex // guards pending and lastRun
	pending map[CounterDoc]map[string]Delta
	lastRun time.Time

	flushMu sync.Mutex // held for the duration of the one flush in progress
}

// add counts one visit for each named counter on doc.
func (c *counters) add(doc CounterDoc, names ...string) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.docLocked(doc)
	for _, name := range names {
		d := p[name]
		d.N++
		d.Last = now
		p[name] = d
	}
}

// merge adds deltas back into the pending counts for doc, keeping the later
// of the two Last times.
func (c *counters) merge(doc CounterDoc, deltas map[string]Delta) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.docLocked(doc)
	for name, d := range deltas {
		cur := p[name]
		cur.N += d.N
		if d.Last.After(cur.Last) {
			cur.Last = d.Last
		}
		p[name] = cur
	}
}

// docLocked returns the pending counters for doc, creating them. c.mu must
// be held.
func (c *counters) docLocked(doc CounterDoc) map[string]Delta {
	if c.pending == nil {
		c.pending = make(map[CounterDoc]map[string]Delta)
	}
	p := c.pending[doc]
	if p == nil {
		p = make(map[string]Delta)
		c.pending[doc] = p
	}
	return p
}

// take removes and returns everything pending. When due is set it returns
// nil unless at least one interval has passed since the last flush.
func (c *counters) take(due bool) map[CounterDoc]map[string]Delta {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.pending) == 0 || due && now.Sub(c.lastRun) < c.interval {
		return nil
	}
	c.lastRun = now
	batch := c.pending
	c.pending = nil
	return batch
}

// start begins a flush in the background when one is due and no other flush
// is running, and returns a function that waits for it. The caller runs the
// wait before its request completes, so the writes overlap the request's own
// work but still happen while the instance has CPU. When nothing is due the
// returned function returns at once.
func (c *counters) start(ctx context.Context) (wait func()) {
	if !c.flushMu.TryLock() {
		return func() {}
	}
	batch := c.take(true)
	if batch == nil {
		c.flushMu.Unlock()
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer c.flushMu.Unlock()
		c.write(context.WithoutCancel(ctx), batch, true)
	}()
	return func() { <-done }
}

// flush writes everything pending, waiting for any flush in progress first.
// It is the last write before the process exits, so failures are logged and
// not requeued.
func (c *counters) flush(ctx context.Context) {
	c.flushMu.Lock()
	defer c.flushMu.Unlock()
	c.write(ctx, c.take(false), false)
}

// write sends one AddCounts per document in parallel, each bounded by the
// write timeout. When requeue is set, failures the store marks with
// ErrCounterRetry are merged back for the next flush. Any other failure is
// dropped, because a write that timed out may still have been applied and
// writing it again would double count.
func (c *counters) write(ctx context.Context, batch map[CounterDoc]map[string]Delta, requeue bool) {
	var wg sync.WaitGroup
	for doc, deltas := range batch {
		wg.Go(func() {
			wctx, cancel := context.WithTimeout(ctx, c.timeout)
			defer cancel()
			err := c.store.AddCounts(wctx, doc, deltas)
			if err == nil {
				return
			}
			retry := requeue && errors.Is(err, ErrCounterRetry)
			c.logger.Warn("analytics write failed", "doc", doc.String(), "requeued", retry, "err", err)
			if retry {
				c.merge(doc, deltas)
			}
		})
	}
	wg.Wait()
}
