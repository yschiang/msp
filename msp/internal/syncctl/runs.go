package syncctl

// This file: in-flight conformance runs, keyed by digest (design D12). A
// reconcile starts a run and returns; later reconciles poll it. One entry per
// digest also gives "at most one run per digest". Results stay in the table
// for the life of the process — the same digest always gets the same verdict
// — and are dropped only on infrastructure error, so the next reconcile
// retries.
// ponytail: unbounded map; evict when the registry DB (Phase 5) becomes the
// record of conformance results.

import (
	"sync"

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
	mu   sync.Mutex
	runs map[string]*run
}

// start returns the run for digest, launching fn in a goroutine on first call.
func (t *runTable) start(digest string, fn func() (*conformance.Report, error)) *run {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.runs == nil {
		t.runs = map[string]*run{}
	}
	if r, ok := t.runs[digest]; ok {
		return r
	}
	r := &run{done: make(chan struct{})}
	t.runs[digest] = r
	go func() {
		r.report, r.err = fn()
		close(r.done)
	}()
	return r
}

func (t *runTable) forget(digest string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.runs, digest)
}
