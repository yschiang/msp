package syncctl

// This file: in-flight conformance runs, keyed by digest (design D12). A
// reconcile starts a run and returns; later reconciles poll it. One entry per
// digest also gives "at most one run per digest". Results stay in the table
// for the life of the process — the same digest always gets the same verdict
// — and are dropped only on infrastructure error, so the next reconcile
// retries.
// ponytail: unbounded map; evict when the registry DB (Phase 5) becomes the
// record of conformance results.
//
// The table also owns the runs' context, so shutdown can cancel them: a run is
// a chain of docker containers and a temp dir under the repo root, all of them
// cleaned up by VerifyImage's deferred cleaner, which never runs if the
// process exits while the run is still going.

import (
	"context"
	"sync"
	"time"

	"github.com/yschiang/msp/msp/internal/conformance"
)

type run struct {
	done   chan struct{}
	report *conformance.Report
	err    error
}

func (r *run) finished() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

type runTable struct {
	mu     sync.Mutex
	runs   map[string]*run
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// baseLocked is the parent context of every run, created on first use. The
// table owns it instead of deriving it from the manager's context because a
// reconcile can start a run before the shutdown runnable is up, and that run
// must be cancellable too.
func (t *runTable) baseLocked() context.Context {
	if t.ctx == nil {
		t.ctx, t.cancel = context.WithCancel(context.Background())
	}
	return t.ctx
}

// start returns the run for digest, launching fn in a goroutine on first call.
func (t *runTable) start(digest string, fn func(ctx context.Context) (*conformance.Report, error)) *run {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.runs == nil {
		t.runs = map[string]*run{}
	}
	if r, ok := t.runs[digest]; ok {
		return r
	}
	ctx := t.baseLocked()
	r := &run{done: make(chan struct{})}
	t.runs[digest] = r
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		r.report, r.err = fn(ctx)
		if err := ctx.Err(); err != nil {
			// A cancelled run is infrastructure, never a verdict: the
			// half-finished report says nothing about the image (a cancelled
			// `docker cp` reads exactly like a missing manifest). The caller
			// sees an error, forgets the digest, and a restarted controller
			// runs it again.
			r.report, r.err = nil, err
		}
		close(r.done)
	}()
	return r
}

func (t *runTable) forget(digest string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.runs, digest)
}

// stop cancels every in-flight run and waits up to timeout for them to return,
// reporting whether they did. Waiting is the point: cancellation only tells
// VerifyImage to give up, its deferred cleanup still needs the time to remove
// the containers and the temp dir.
func (t *runTable) stop(timeout time.Duration) bool {
	t.mu.Lock()
	t.baseLocked() // a table nothing ever used still gets a cancel to call
	t.cancel()
	t.mu.Unlock()

	done := make(chan struct{})
	go func() { t.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}
