package traffic

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
	"github.com/yschiang/msp/msp/internal/stub"
	"google.golang.org/grpc"
)

// exampleGoldenDir is the real, committed golden fixture (Task 4/5), reused
// here the same way envelope's own tests reuse it.
const exampleGoldenDir = "../../../contract/examples/defect-cls/golden"

// recordingServer wraps a servingv1.ModelServiceServer and records every
// PredictRequest it receives, guarded by a mutex: gRPC serves each RPC on
// its own goroutine (its connection pool), so this is exactly the kind of
// shared state Predict's own doc comment warns must synchronize itself.
// stub.Server.PayloadFunc does not fit this job -- its signature is
// func(base []byte) []byte, with no device_id parameter -- so recording
// happens in a thin wrapper around the stub instead.
type recordingServer struct {
	servingv1.UnimplementedModelServiceServer
	inner servingv1.ModelServiceServer

	mu  sync.Mutex
	got []*servingv1.PredictRequest
}

func (s *recordingServer) Predict(ctx context.Context, req *servingv1.PredictRequest) (*servingv1.PredictResponse, error) {
	s.mu.Lock()
	s.got = append(s.got, req)
	s.mu.Unlock()
	return s.inner.Predict(ctx, req)
}

func (s *recordingServer) Health(ctx context.Context, req *servingv1.HealthRequest) (*servingv1.HealthResponse, error) {
	return s.inner.Health(ctx, req)
}

func (s *recordingServer) deviceIDs() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := make(map[string]int)
	for _, r := range s.got {
		counts[r.DeviceId]++
	}
	return counts
}

// startTestServer serves srv over a real TCP loopback listener (ephemeral
// port) and returns its address. envelope.Dial -- the only entry point this
// package can reach from outside the envelope package -- takes a plain
// host:port target with no injectable dialer, so bufconn (which needs
// grpc.WithContextDialer, an unexported seam of envelope.dial) is not
// reachable here; a real listener is the available option.
func startTestServer(t *testing.T, srv servingv1.ModelServiceServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer()
	servingv1.RegisterModelServiceServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	return lis.Addr().String()
}

func newStubServer(t *testing.T) *stub.Server {
	t.Helper()
	srv, err := stub.NewServer("")
	if err != nil {
		t.Fatalf("stub.NewServer: %v", err)
	}
	return srv
}

// TestRunRandomPayloadAllOK is the brief's Step 1 test: n=20, concurrency=4,
// random payloads against internal/stub -> OK==20 and at least 2 distinct
// device ids observed server-side.
func TestRunRandomPayloadAllOK(t *testing.T) {
	rec := &recordingServer{inner: newStubServer(t)}
	addr := startTestServer(t, rec)

	cfg := Config{
		Target:             addr,
		Model:              "defect-cls",
		N:                  20,
		Concurrency:        4,
		RandomPayloadBytes: 16,
		DeviceCount:        10,
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Sent != 20 {
		t.Errorf("Sent = %d, want 20", res.Sent)
	}
	if res.OK != 20 {
		t.Errorf("OK = %d, want 20", res.OK)
	}
	if res.InvalidInput != 0 || res.InternalError != 0 || res.TransportErr != 0 {
		t.Errorf("InvalidInput=%d InternalError=%d TransportErr=%d, want all 0", res.InvalidInput, res.InternalError, res.TransportErr)
	}
	if n := len(rec.deviceIDs()); n < 2 {
		t.Errorf("server observed %d distinct device ids, want >= 2", n)
	}
	if res.Failed(false) {
		t.Error("Failed(false) = true, want false (random mode, no transport error)")
	}
}

// TestRunDeviceRoundRobinIsDeterministic pins the exact device_id
// distribution ruling 3 requires: device id depends only on job index
// (idx % DeviceCount), never on completion order, so the counts are the same
// every run regardless of goroutine scheduling.
func TestRunDeviceRoundRobinIsDeterministic(t *testing.T) {
	rec := &recordingServer{inner: newStubServer(t)}
	addr := startTestServer(t, rec)

	cfg := Config{
		Target:             addr,
		Model:              "defect-cls",
		N:                  10,
		Concurrency:        4,
		RandomPayloadBytes: 8,
		DeviceCount:        3,
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.OK != 10 {
		t.Fatalf("OK = %d, want 10", res.OK)
	}

	// idx 0..9 mod 3: dev-000 at 0,3,6,9 (4x); dev-001 at 1,4,7 (3x); dev-002
	// at 2,5,8 (3x). This exact partition is what "deterministic, not
	// random" round robin means -- it must hold every run.
	want := map[string]int{"dev-000": 4, "dev-001": 3, "dev-002": 3}
	got := rec.deviceIDs()
	if len(got) != len(want) {
		t.Fatalf("device ids observed = %v, want %v", got, want)
	}
	for id, count := range want {
		if got[id] != count {
			t.Errorf("device id %s seen %d times, want %d (got %v)", id, got[id], count, got)
		}
	}
}

// TestRunGoldenModeCyclesInputsDeterministically proves ruling 4 (payloads
// cycle through loaded golden inputs) using the real committed defect-cls
// golden fixture: 2 samples, 5 requests, so each is sent either 3 or 2
// times, cycled by job index.
func TestRunGoldenModeCyclesInputsDeterministically(t *testing.T) {
	rec := &recordingServer{inner: newStubServer(t)}
	addr := startTestServer(t, rec)

	cfg := Config{
		Target:      addr,
		Model:       "defect-cls",
		N:           5,
		Concurrency: 2,
		GoldenDir:   exampleGoldenDir,
		DeviceCount: 10,
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.OK != 5 {
		t.Fatalf("OK = %d, want 5 (defect-cls golden inputs are non-empty, so the stub echoes OK)", res.OK)
	}
	if res.Failed(true) {
		t.Error("Failed(true) = true, want false: golden inputs are valid, every response is OK")
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	payloadCounts := map[string]int{}
	for _, r := range rec.got {
		payloadCounts[string(r.Payload)]++
	}
	if len(payloadCounts) != 2 {
		t.Fatalf("observed %d distinct payloads, want 2 (the two golden samples)", len(payloadCounts))
	}
	// idx 0..4 mod 2: sample 0 at 0,2,4 (3x); sample 1 at 1,3 (2x).
	counts := make([]int, 0, 2)
	for _, c := range payloadCounts {
		counts = append(counts, c)
	}
	sort.Ints(counts)
	if counts[0] != 2 || counts[1] != 3 {
		t.Errorf("payload counts = %v, want [2 3] (5 requests over 2 golden inputs, cycled by index)", counts)
	}
}

// TestRunGoldenModeNonOKFailsExitRule proves the golden-mode half of ruling
// 1: a golden input that the target rejects (here, an empty sample -- the
// stub's own validation rule) makes Failed(true) report a failure, even
// though every Predict call completed without a transport error.
func TestRunGoldenModeNonOKFailsExitRule(t *testing.T) {
	dir := t.TempDir()
	// stub.Server rejects empty payloads with INVALID_INPUT (its only
	// validation rule), so an empty golden sample is a real, reproducible
	// way to make golden mode observe a non-OK response without touching
	// stub internals.
	if err := os.WriteFile(filepath.Join(dir, "sample-01.bin"), nil, 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "expected-01.bin"), []byte{0x01}, 0o644); err != nil {
		t.Fatalf("write expected: %v", err)
	}

	rec := &recordingServer{inner: newStubServer(t)}
	addr := startTestServer(t, rec)

	cfg := Config{
		Target:      addr,
		Model:       "defect-cls",
		N:           3,
		Concurrency: 1,
		GoldenDir:   dir,
		DeviceCount: 10,
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.InvalidInput != 3 {
		t.Fatalf("InvalidInput = %d, want 3 (empty golden payload rejected by the stub)", res.InvalidInput)
	}
	if res.TransportErr != 0 {
		t.Errorf("TransportErr = %d, want 0", res.TransportErr)
	}
	if !res.Failed(true) {
		t.Error("Failed(true) = false, want true: golden mode with a non-OK response must fail")
	}
}

// stoppingServer wraps a Predict implementation and, after stopAfter calls
// have completed, invokes onStop exactly once. The counter is guarded by its
// own mutex so it is safe under the server's per-RPC goroutines -- unlike
// mutating a server's fields after grpc.Server.Serve has already started
// accepting connections, which the race detector cannot see as synchronized
// even when it happens to be safe in practice.
type stoppingServer struct {
	servingv1.UnimplementedModelServiceServer
	inner     servingv1.ModelServiceServer
	stopAfter int
	onStop    func()

	mu      sync.Mutex
	count   int
	stopped bool
}

func (s *stoppingServer) Predict(ctx context.Context, req *servingv1.PredictRequest) (*servingv1.PredictResponse, error) {
	resp, err := s.inner.Predict(ctx, req)
	s.mu.Lock()
	s.count++
	fire := s.count == s.stopAfter && !s.stopped
	if fire {
		s.stopped = true
	}
	s.mu.Unlock()
	if fire {
		go s.onStop()
	}
	return resp, err
}

func (s *stoppingServer) Health(ctx context.Context, req *servingv1.HealthRequest) (*servingv1.HealthResponse, error) {
	return s.inner.Health(ctx, req)
}

// TestRunTransportErrorFailsExitRuleRegardlessOfMode proves the other half
// of ruling 1: stopping the server mid-run forces some in-flight Predict
// calls to fail at the transport level, and that alone must fail the exit
// rule even in random-payload mode, where a non-OK status by itself would
// not.
func TestRunTransportErrorFailsExitRuleRegardlessOfMode(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer()
	srv := &stoppingServer{inner: newStubServer(t), stopAfter: 5}
	srv.onStop = func() { gs.Stop() }
	servingv1.RegisterModelServiceServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)

	cfg := Config{
		Target:             lis.Addr().String(),
		Model:              "defect-cls",
		N:                  40,
		Concurrency:        5,
		RandomPayloadBytes: 8,
		DeviceCount:        10,
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.TransportErr == 0 {
		t.Fatal("TransportErr = 0, want > 0 (server was stopped mid-run)")
	}
	if !res.Failed(false) {
		t.Error("Failed(false) = false, want true: a transport error must fail the exit rule even in random-payload mode")
	}
}

// predictServer adapts a plain function to the one method this test needs
// from servingv1.ModelServiceServer; embedding UnimplementedModelServiceServer
// satisfies the interface's forward-compatibility marker method.
type predictServer struct {
	servingv1.UnimplementedModelServiceServer
	predict func(context.Context, *servingv1.PredictRequest) (*servingv1.PredictResponse, error)
}

func (s predictServer) Predict(ctx context.Context, req *servingv1.PredictRequest) (*servingv1.PredictResponse, error) {
	return s.predict(ctx, req)
}
func (s predictServer) Health(context.Context, *servingv1.HealthRequest) (*servingv1.HealthResponse, error) {
	return &servingv1.HealthResponse{Ready: true}, nil
}

// TestRunRespectsContextCancellation proves ruling 6: a context cancelled
// mid-run stops issuing new requests promptly instead of running to cfg.N.
// The server sleeps briefly per request so N=1000 would otherwise take far
// longer than the cancellation deadline. It also proves the Important-1 fix:
// an incomplete run (Sent < cfg.N) must not report success by returning a
// nil error -- Run returns ctx.Err() instead, exactly the "non-zero exit
// signal" main.go relies on for an incomplete run that isn't already caught
// by Failed's outcome counters (there are no outcomes to count for a request
// never sent).
func TestRunRespectsContextCancellation(t *testing.T) {
	inner := newStubServer(t)
	slow := predictServer{predict: func(ctx context.Context, req *servingv1.PredictRequest) (*servingv1.PredictResponse, error) {
		time.Sleep(20 * time.Millisecond)
		return inner.Predict(ctx, req)
	}}
	addr := startTestServer(t, slow)

	cfg := Config{
		Target:             addr,
		Model:              "defect-cls",
		N:                  1000,
		Concurrency:        4,
		RandomPayloadBytes: 8,
		DeviceCount:        10,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := Run(ctx, cfg)
	elapsed := time.Since(start)

	if res.Sent >= 1000 {
		t.Errorf("Sent = %d, want < 1000 (ctx should have stopped the run early)", res.Sent)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Run took %v after a 150ms ctx timeout, want it to return promptly", elapsed)
	}
	if err == nil {
		t.Error("Run: want a non-nil error for an incomplete run (Sent < N), got nil -- this is the silent-success shape")
	}
}

// TestRunAlreadyCancelledContextIsNotSilentSuccess is the sharpest case of
// Important-1: ctx is cancelled *before* Run is even called, so nothing is
// ever sent. Without the Sent<cfg.N check, Result is all-zero, Failed(false)
// and Failed(true) both report false, and main.go would exit 0 having sent
// nothing -- Task 9's whole point is that exit code is trustworthy. Run must
// surface this as an error instead.
func TestRunAlreadyCancelledContextIsNotSilentSuccess(t *testing.T) {
	rec := &recordingServer{inner: newStubServer(t)}
	addr := startTestServer(t, rec)

	cfg := Config{
		Target:             addr,
		Model:              "defect-cls",
		N:                  5,
		Concurrency:        2,
		RandomPayloadBytes: 8,
		DeviceCount:        10,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before Run ever sees it

	res, err := Run(ctx, cfg)
	if err == nil {
		t.Fatal("Run with an already-cancelled ctx: want a non-nil error, got nil")
	}
	// select among ready cases is not ordered, so a worker that happens to
	// already be receiving when the feeder's select runs can still win a job
	// or two against the also-ready ctx.Done() case -- the guarantee is that
	// the feeder stops well short of cfg.N, not that it sends exactly zero.
	if res.Sent >= cfg.N {
		t.Errorf("Sent = %d, want < %d (the feeder should stop almost immediately on an already-cancelled ctx)", res.Sent, cfg.N)
	}
	if res.Failed(false) || res.Failed(true) {
		t.Fatal("Failed() alone reports false for a near-all-zero Result -- the error return, not Failed, is what must catch this case")
	}
}

// badStatusServer always returns a Status value outside the three
// servingv1.Status defines today, simulating what a future wire-contract
// extension (or a misbehaving target) looks like from the client's side.
type badStatusServer struct {
	servingv1.UnimplementedModelServiceServer
}

func (badStatusServer) Predict(_ context.Context, req *servingv1.PredictRequest) (*servingv1.PredictResponse, error) {
	return &servingv1.PredictResponse{
		RequestId: req.RequestId,
		ModelName: req.ModelName,
		Status:    servingv1.Status(99),
	}, nil
}

func (badStatusServer) Health(context.Context, *servingv1.HealthRequest) (*servingv1.HealthResponse, error) {
	return &servingv1.HealthResponse{Ready: true}, nil
}

// TestRunUnknownStatusCountsAsInternalError is Important-2: a Status this
// build doesn't recognize must not vanish from every counter. It must land
// somewhere -- InternalError, since an unrecognized status is the target's
// fault -- so Sent stays equal to the sum of the four outcome counters and
// golden mode's Failed check still catches it.
func TestRunUnknownStatusCountsAsInternalError(t *testing.T) {
	addr := startTestServer(t, badStatusServer{})

	cfg := Config{
		Target:             addr,
		Model:              "defect-cls",
		N:                  3,
		Concurrency:        1,
		RandomPayloadBytes: 8,
		DeviceCount:        10,
	}
	res, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.InternalError != 3 {
		t.Errorf("InternalError = %d, want 3 (an out-of-range Status must count as InternalError, not vanish)", res.InternalError)
	}
	if res.OK != 0 || res.InvalidInput != 0 {
		t.Errorf("OK=%d InvalidInput=%d, want both 0", res.OK, res.InvalidInput)
	}
	if sum := res.OK + res.InvalidInput + res.InternalError + res.TransportErr; sum != res.Sent {
		t.Errorf("counters sum to %d, want Sent %d (every sent request must land in exactly one counter)", sum, res.Sent)
	}
	if !res.Failed(true) {
		t.Error("Failed(true) = false, want true: golden mode must fail on a status it cannot classify as OK")
	}
}

func TestResultFailed(t *testing.T) {
	tests := []struct {
		name       string
		result     Result
		goldenMode bool
		want       bool
	}{
		{"all zero, random mode", Result{Sent: 10, OK: 10}, false, false},
		{"all zero, golden mode", Result{Sent: 10, OK: 10}, true, false},
		{"transport error, random mode", Result{Sent: 10, OK: 9, TransportErr: 1}, false, true},
		{"transport error, golden mode", Result{Sent: 10, OK: 9, TransportErr: 1}, true, true},
		{"invalid input only, random mode", Result{Sent: 10, OK: 9, InvalidInput: 1}, false, false},
		{"invalid input only, golden mode", Result{Sent: 10, OK: 9, InvalidInput: 1}, true, true},
		{"internal error only, random mode", Result{Sent: 10, OK: 9, InternalError: 1}, false, false},
		{"internal error only, golden mode", Result{Sent: 10, OK: 9, InternalError: 1}, true, true},
		{"transport error and invalid input, random mode", Result{Sent: 10, OK: 8, InvalidInput: 1, TransportErr: 1}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.Failed(tt.goldenMode); got != tt.want {
				t.Errorf("Failed(%v) = %v, want %v", tt.goldenMode, got, tt.want)
			}
		})
	}
}

func TestPercentiles(t *testing.T) {
	ms := func(vals ...int) []time.Duration {
		out := make([]time.Duration, len(vals))
		for i, v := range vals {
			out[i] = time.Duration(v) * time.Millisecond
		}
		return out
	}

	tests := []struct {
		name      string
		latencies []time.Duration
		wantP50   int
		wantP99   int
	}{
		{"empty", nil, 0, 0},
		{"single sample", ms(5), 5, 5},
		// nearest-rank over an unsorted input, n=10: idx = ceil(p/100*10)-1.
		// p50 -> ceil(5.0)-1 = 4 -> 5th smallest (50ms). p99 -> ceil(9.9)-1 =
		// 9 -> the max (100ms). Small-n P99 landing on the max is expected
		// for nearest-rank, not a bug.
		{"ten samples", ms(100, 10, 90, 20, 80, 30, 70, 40, 60, 50), 50, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p50, p99 := percentiles(tt.latencies)
			if p50 != time.Duration(tt.wantP50)*time.Millisecond {
				t.Errorf("p50 = %v, want %dms", p50, tt.wantP50)
			}
			if p99 != time.Duration(tt.wantP99)*time.Millisecond {
				t.Errorf("p99 = %v, want %dms", p99, tt.wantP99)
			}
		})
	}
}

func TestRunRejectsInvalidConfig(t *testing.T) {
	base := Config{Target: "127.0.0.1:1", Model: "m", N: 1, Concurrency: 1, DeviceCount: 1, RandomPayloadBytes: 8}

	tests := []struct {
		name   string
		mutate func(c Config) Config
	}{
		{"n=0", func(c Config) Config { c.N = 0; return c }},
		{"concurrency=0", func(c Config) Config { c.Concurrency = 0; return c }},
		{"device-count=0", func(c Config) Config { c.DeviceCount = 0; return c }},
		{"neither golden-dir nor random-payload-bytes", func(c Config) Config { c.RandomPayloadBytes = 0; return c }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Run(context.Background(), tt.mutate(base))
			if err == nil {
				t.Fatal("Run: want error, got nil")
			}
		})
	}
}

func TestLoadGoldenPayloadsRejectsEmptyDir(t *testing.T) {
	if _, err := loadGoldenPayloads(t.TempDir()); err == nil {
		t.Fatal("loadGoldenPayloads on an empty dir: want error, got nil")
	}
}
