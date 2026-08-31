package stub

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// startBufServer serves srv over an in-memory bufconn listener and returns a
// real ModelServiceClient dialed against it, matching the pattern in
// internal/envelope/client_test.go: tests exercise the stub over a real gRPC
// channel, not by calling the servicer method directly.
func startBufServer(t *testing.T, srv servingv1.ModelServiceServer) servingv1.ModelServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	servingv1.RegisterModelServiceServer(gs, srv)
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return servingv1.NewModelServiceClient(conn)
}

func TestPredictValidRequestEchoesPayload(t *testing.T) {
	srv, err := NewServer("")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	client := startBufServer(t, srv)

	payload := []byte{0x01, 0x02, 0x03}
	resp, err := client.Predict(context.Background(), &servingv1.PredictRequest{
		RequestId: "req-1",
		ModelName: "defect-cls",
		Payload:   payload,
	})
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if resp.Status != servingv1.Status_OK {
		t.Errorf("Status = %v, want OK", resp.Status)
	}
	if resp.ModelVersion != DefaultVersion {
		t.Errorf("ModelVersion = %q, want %q", resp.ModelVersion, DefaultVersion)
	}
	if resp.ModelDigest != "" {
		t.Errorf("ModelDigest = %q, want empty", resp.ModelDigest)
	}
	if string(resp.Payload) != string(payload) {
		t.Errorf("Payload = %v, want echoed %v", resp.Payload, payload)
	}
	if resp.RequestId != "req-1" {
		t.Errorf("RequestId = %q, want %q", resp.RequestId, "req-1")
	}
	if resp.ModelName != "defect-cls" {
		t.Errorf("ModelName = %q, want %q", resp.ModelName, "defect-cls")
	}
}

func TestPredictInvalidInput(t *testing.T) {
	srv, err := NewServer("")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	client := startBufServer(t, srv)

	cases := []struct {
		name string
		req  *servingv1.PredictRequest
	}{
		{"empty request_id", &servingv1.PredictRequest{RequestId: "", ModelName: "m", Payload: []byte{1}}},
		{"empty model_name", &servingv1.PredictRequest{RequestId: "r", ModelName: "", Payload: []byte{1}}},
		{"empty payload", &servingv1.PredictRequest{RequestId: "r", ModelName: "m", Payload: nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.Predict(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("Predict: %v", err)
			}
			if resp.Status != servingv1.Status_INVALID_INPUT {
				t.Errorf("Status = %v, want INVALID_INPUT", resp.Status)
			}
		})
	}
}

func TestPredictResponseFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resp.bin")
	fileBytes := []byte{0xaa, 0xbb, 0xcc, 0xdd}
	if err := os.WriteFile(path, fileBytes, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	srv, err := NewServer(path)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	client := startBufServer(t, srv)

	// Send a request payload that differs from the file bytes, so a pass here
	// proves the response comes from the file, not from echoing the request.
	for i := 0; i < 2; i++ {
		resp, err := client.Predict(context.Background(), &servingv1.PredictRequest{
			RequestId: "req-file",
			ModelName: "defect-cls",
			Payload:   []byte{0x99},
		})
		if err != nil {
			t.Fatalf("Predict: %v", err)
		}
		if resp.Status != servingv1.Status_OK {
			t.Errorf("Status = %v, want OK", resp.Status)
		}
		if string(resp.Payload) != string(fileBytes) {
			t.Errorf("Payload = %v, want response-file bytes %v", resp.Payload, fileBytes)
		}
	}
}

func TestNewServerResponseFileUnreadable(t *testing.T) {
	if _, err := NewServer(filepath.Join(t.TempDir(), "does-not-exist.bin")); err == nil {
		t.Fatal("NewServer with unreadable response-file: want error, got nil")
	}
}

func TestHealthAlwaysReady(t *testing.T) {
	srv, err := NewServer("")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	client := startBufServer(t, srv)

	resp, err := client.Health(context.Background(), &servingv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !resp.Ready {
		t.Error("Ready = false, want true")
	}
}

// TestVersionOverride exercises the hook Task 8 needs to simulate a
// wrong-version misbehavior: Version is an exported field, settable after
// construction, with no CLI flag exposing it.
func TestVersionOverride(t *testing.T) {
	srv, err := NewServer("")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.Version = "wrong-version"
	client := startBufServer(t, srv)

	resp, err := client.Predict(context.Background(), &servingv1.PredictRequest{
		RequestId: "req-1", ModelName: "m", Payload: []byte{1},
	})
	if err != nil {
		t.Fatalf("Predict: %v", err)
	}
	if resp.ModelVersion != "wrong-version" {
		t.Errorf("ModelVersion = %q, want %q", resp.ModelVersion, "wrong-version")
	}
}

// TestPayloadFuncOverride exercises the hook Task 8 needs to simulate a
// non-deterministic payload: PayloadFunc, if set, replaces the normally
// computed payload (echo or response-file bytes) on every call.
func TestPayloadFuncOverride(t *testing.T) {
	srv, err := NewServer("")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	n := byte(0)
	srv.PayloadFunc = func(base []byte) []byte {
		n++
		return []byte{n}
	}
	client := startBufServer(t, srv)

	seen := map[byte]bool{}
	for i := 0; i < 3; i++ {
		resp, err := client.Predict(context.Background(), &servingv1.PredictRequest{
			RequestId: "req-1", ModelName: "m", Payload: []byte{0xff},
		})
		if err != nil {
			t.Fatalf("Predict %d: %v", i, err)
		}
		if len(resp.Payload) != 1 {
			t.Fatalf("Payload = %v, want 1 byte", resp.Payload)
		}
		seen[resp.Payload[0]] = true
	}
	if len(seen) != 3 {
		t.Errorf("saw %d distinct payloads across 3 calls, want 3 (non-deterministic)", len(seen))
	}
}
