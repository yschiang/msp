package envelope

import (
	"context"
	"errors"
	"net"
	"regexp"
	"sync"
	"testing"
	"time"

	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// fakeModelServer records every PredictRequest it receives so the test can
// assert on the envelope the client actually put on the wire.
type fakeModelServer struct {
	servingv1.UnimplementedModelServiceServer
	mu  sync.Mutex
	got []*servingv1.PredictRequest
}

func (s *fakeModelServer) Predict(_ context.Context, req *servingv1.PredictRequest) (*servingv1.PredictResponse, error) {
	s.mu.Lock()
	s.got = append(s.got, req)
	s.mu.Unlock()
	return &servingv1.PredictResponse{
		RequestId: req.RequestId,
		ModelName: req.ModelName,
		Status:    servingv1.Status_OK,
		Payload:   req.Payload,
	}, nil
}

func (s *fakeModelServer) Health(context.Context, *servingv1.HealthRequest) (*servingv1.HealthResponse, error) {
	return &servingv1.HealthResponse{Ready: true, Detail: "ok"}, nil
}

// startBufServer serves fakeModelServer over an in-memory bufconn listener and
// returns a connected Client (via the same dial path Dial uses).
func startBufServer(t *testing.T) (*fakeModelServer, *Client) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	fake := &fakeModelServer{}
	servingv1.RegisterModelServiceServer(srv, fake)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	c, err := Dial("passthrough:///bufnet", 2*time.Second,
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}))
	if err != nil {
		t.Fatalf("dial over bufconn: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return fake, c
}

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestPredictFillsEnvelope(t *testing.T) {
	fake, c := startBufServer(t)

	payload := []byte{0xde, 0xad}
	before := time.Now()
	resp, err := c.Predict(context.Background(), "defect-cls", "dev-7", payload)
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	after := time.Now()
	if resp.Status != servingv1.Status_OK {
		t.Errorf("Status = %v, want OK", resp.Status)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.got) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(fake.got))
	}
	req := fake.got[0]
	if req.RequestId == "" {
		t.Error("RequestId is empty")
	}
	if !uuidV4Re.MatchString(req.RequestId) {
		t.Errorf("RequestId %q is not a v4 UUID", req.RequestId)
	}
	if req.IngestTime == nil {
		t.Fatal("IngestTime is not set")
	}
	ts := req.IngestTime.AsTime()
	if ts.Before(before.Add(-time.Second)) || ts.After(after.Add(time.Second)) {
		t.Errorf("IngestTime %v outside call window [%v, %v]", ts, before, after)
	}
	if req.ModelName != "defect-cls" {
		t.Errorf("ModelName = %q, want %q", req.ModelName, "defect-cls")
	}
	if req.DeviceId != "dev-7" {
		t.Errorf("DeviceId = %q, want %q", req.DeviceId, "dev-7")
	}
	if string(req.Payload) != string(payload) {
		t.Errorf("Payload = %v, want %v", req.Payload, payload)
	}
	if resp.RequestId != req.RequestId {
		t.Errorf("response RequestId %q != request %q", resp.RequestId, req.RequestId)
	}
}

func TestPredictGeneratesFreshRequestIDs(t *testing.T) {
	fake, c := startBufServer(t)
	for i := 0; i < 3; i++ {
		if _, err := c.Predict(context.Background(), "defect-cls", "dev-7", nil); err != nil {
			t.Fatalf("Predict %d: %v", i, err)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	seen := map[string]bool{}
	for _, req := range fake.got {
		if seen[req.RequestId] {
			t.Fatalf("RequestId %q reused across calls", req.RequestId)
		}
		seen[req.RequestId] = true
	}
}

func TestHealth(t *testing.T) {
	_, c := startBufServer(t)
	resp, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !resp.Ready {
		t.Error("Ready = false, want true")
	}
	if resp.Detail != "ok" {
		t.Errorf("Detail = %q, want %q", resp.Detail, "ok")
	}
}

func TestDialTimesOutWhenUnreachable(t *testing.T) {
	start := time.Now()
	_, err := Dial("passthrough:///nowhere", 200*time.Millisecond,
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return nil, errors.New("no route")
		}))
	if err == nil {
		t.Fatal("want error dialing unreachable target, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("dial took %v, should give up around the 200ms timeout", elapsed)
	}
}
