// Package traffic implements the synthetic load driver for MSP Phase 0
// (spec §11): it drives PredictRequests at a target ModelService over the
// envelope client, either cycling golden inputs or sending random payloads,
// and reports per-outcome counters plus latency percentiles. It is the
// integration target Task 9's acceptance script runs twice -- once against
// router-stub (MYSVC's predict round-trip stand-in), once against the real
// defect-cls example model -- and both runs must exit 0.
package traffic

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
	"github.com/yschiang/msp/msp/internal/envelope"
	"github.com/yschiang/msp/msp/internal/manifest"
)

// defaultDialTimeout bounds envelope.Dial when Config.DialTimeout is unset.
// Not a CLI flag -- the brief's flag list does not list one.
const defaultDialTimeout = 5 * time.Second

// Config is the whole knob surface for a Run.
type Config struct {
	Target      string
	Model       string
	N           int
	Concurrency int

	// GoldenDir, if non-empty, selects golden mode: payloads cycle through
	// the sample-*/expected-* pairs discovered in this directory (see
	// loadGoldenPayloads). Takes precedence over RandomPayloadBytes.
	GoldenDir string

	// RandomPayloadBytes selects random mode when GoldenDir is empty: every
	// request gets a freshly generated payload of this length.
	RandomPayloadBytes int

	// DeviceCount is the round-robin device pool size; device ids are
	// dev-000 .. dev-{DeviceCount-1}.
	DeviceCount int

	// DialTimeout bounds envelope.Dial. Zero means defaultDialTimeout.
	DialTimeout time.Duration
}

// Result is the summary of one Run: how many requests landed in each of the
// four outcomes the envelope client makes visible, and latency percentiles
// over the requests that got a response at all (see percentiles).
type Result struct {
	Sent, OK, InvalidInput, InternalError, TransportErr int
	P50, P99                                            time.Duration
}

// Failed applies the controller's exit-code rule: a transport error is
// always a failure; in golden mode, a non-OK response is too, because
// goldens are known-good inputs so INVALID_INPUT or INTERNAL_ERROR means the
// target actually misbehaved. In random-payload mode a non-OK is not by
// itself a failure -- random bytes may legitimately fail model or envelope
// validation, and that is expected, not a defect.
func (r Result) Failed(goldenMode bool) bool {
	if r.TransportErr > 0 {
		return true
	}
	return goldenMode && (r.InvalidInput > 0 || r.InternalError > 0)
}

// outcome is one worker's Predict result, handed to the single goroutine
// that aggregates into Result over a channel -- so the counters and latency
// slice need no lock (single-owner, not shared-and-guarded).
type outcome struct {
	transportErr bool
	status       servingv1.Status
	latency      time.Duration
}

// Run dials cfg.Target once -- a *grpc.ClientConn is safe for concurrent
// use, and more connections is not obviously better -- and drives cfg.N
// Predict calls across cfg.Concurrency workers pulling from a shared job
// index feed, then aggregates results from a single goroutine. It respects
// ctx: a cancelled context stops the job feeder from handing out more work
// and cancels any Predict already in flight, so Run returns promptly with
// Sent < cfg.N instead of running to completion.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if cfg.N <= 0 {
		return Result{}, fmt.Errorf("n must be > 0, got %d", cfg.N)
	}
	if cfg.Concurrency <= 0 {
		return Result{}, fmt.Errorf("concurrency must be > 0, got %d", cfg.Concurrency)
	}
	if cfg.DeviceCount <= 0 {
		return Result{}, fmt.Errorf("device-count must be > 0, got %d", cfg.DeviceCount)
	}

	var goldenPayloads [][]byte
	if cfg.GoldenDir != "" {
		payloads, err := loadGoldenPayloads(cfg.GoldenDir)
		if err != nil {
			return Result{}, err
		}
		goldenPayloads = payloads
	} else if cfg.RandomPayloadBytes <= 0 {
		return Result{}, fmt.Errorf("random-payload-bytes must be > 0 when golden-dir is unset, got %d", cfg.RandomPayloadBytes)
	}

	timeout := cfg.DialTimeout
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}
	client, err := envelope.Dial(cfg.Target, timeout)
	if err != nil {
		return Result{}, fmt.Errorf("dial %s: %w", cfg.Target, err)
	}
	defer client.Close()

	// Job feeder: hands out indices 0..N-1. Index (not arrival order) drives
	// device id and golden payload selection, so both stay deterministic
	// regardless of goroutine scheduling. Stops early, without sending the
	// remaining indices, when ctx is cancelled.
	jobs := make(chan int)
	go func() {
		defer close(jobs)
		for i := 0; i < cfg.N; i++ {
			select {
			case <-ctx.Done():
				return
			case jobs <- i:
			}
		}
	}()

	results := make(chan outcome)
	var wg sync.WaitGroup
	for w := 0; w < cfg.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				deviceID := fmt.Sprintf("dev-%03d", idx%cfg.DeviceCount)
				payload := payloadFor(cfg, goldenPayloads, idx)
				start := time.Now()
				resp, err := client.Predict(ctx, cfg.Model, deviceID, payload)
				elapsed := time.Since(start)
				if err != nil {
					results <- outcome{transportErr: true}
					continue
				}
				results <- outcome{status: resp.Status, latency: elapsed}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	var res Result
	var latencies []time.Duration
	for o := range results {
		res.Sent++
		if o.transportErr {
			res.TransportErr++
			continue
		}
		latencies = append(latencies, o.latency)
		switch o.status {
		case servingv1.Status_OK:
			res.OK++
		case servingv1.Status_INVALID_INPUT:
			res.InvalidInput++
		case servingv1.Status_INTERNAL_ERROR:
			res.InternalError++
		}
	}
	res.P50, res.P99 = percentiles(latencies)
	return res, nil
}

// payloadFor returns the payload for job index idx: the idx-th golden input
// cycled modulo the loaded set in golden mode, or a freshly generated random
// payload otherwise. crypto/rand.Read is safe for concurrent use (no shared
// mutable state a caller must guard, unlike stub.Server.PayloadFunc), so each
// worker can call this directly with no locking of its own.
func payloadFor(cfg Config, goldens [][]byte, idx int) []byte {
	if len(goldens) > 0 {
		return goldens[idx%len(goldens)]
	}
	payload := make([]byte, cfg.RandomPayloadBytes)
	_, _ = rand.Read(payload) // crypto/rand.Read only errors if the OS entropy source is broken
	return payload
}

// loadGoldenPayloads discovers sample-*/expected-* pairs in dir (the naming
// convention every Phase 0 golden fixture follows -- see
// contract/examples/defect-cls/make_goldens.py) and loads them through
// envelope.LoadGoldens, so payload loading is shared with the conformance
// probe rather than reimplemented here.
//
// LoadGoldens takes a *manifest.Manifest; this tool has no -manifest flag
// (only -golden-dir), so there is no real model-manifest.yaml to load, and a
// minimal Manifest is synthesized here from the discovered file pairs
// instead. LoadGoldens also requires each pair's expected-output file to
// exist, even though traffic generation never reads GoldenPair.Expected
// (verifying outputs against expected is Compare's job, Task 8's, not this
// tool's) -- that requirement doubles as a sanity check that the golden dir
// is well-formed.
func loadGoldenPayloads(dir string) ([][]byte, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "sample-*"))
	if err != nil {
		return nil, fmt.Errorf("glob golden dir %s: %w", dir, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no golden samples found in %s (expected sample-* files)", dir)
	}
	sort.Strings(matches) // deterministic cycling order

	m := &manifest.Manifest{}
	for _, match := range matches {
		base := filepath.Base(match)
		expected := "expected" + strings.TrimPrefix(base, "sample")
		m.GoldenSamples = append(m.GoldenSamples, manifest.GoldenSample{
			Input:  base,
			Output: expected,
		})
	}

	pairs, err := envelope.LoadGoldens(m, dir)
	if err != nil {
		return nil, err
	}
	payloads := make([][]byte, len(pairs))
	for i, p := range pairs {
		payloads[i] = p.Input
	}
	return payloads, nil
}

// percentiles computes P50/P99 over successful latencies (requests that got
// a response at all, whatever its Status -- transport failures contribute no
// sample since no round trip completed) using the nearest-rank method: sort
// ascending, index = ceil(p/100 * n) - 1, clamped to [0, n-1]. Not
// interpolated -- with the small n Task 9 runs (order tens), nearest-rank
// always names an actually-observed latency, which is easier to reason about
// than a value interpolated between two samples. A consequence worth noting:
// for small n, P99 lands on the maximum observed sample.
func percentiles(latencies []time.Duration) (p50, p99 time.Duration) {
	if len(latencies) == 0 {
		return 0, 0
	}
	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return nearestRank(sorted, 50), nearestRank(sorted, 99)
}

// nearestRank returns sorted[idx] for idx = ceil(p/100 * len(sorted)) - 1,
// clamped into range. sorted must be ascending and non-empty.
func nearestRank(sorted []time.Duration, p int) time.Duration {
	idx := int(math.Ceil(float64(p)/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
