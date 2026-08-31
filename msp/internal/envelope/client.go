// Package envelope is the shared gRPC client for the MSP serving envelope
// (MSP-SPEC-001 §4.1) plus golden-sample comparison. It fills the envelope
// fields callers must never forge by hand (request_id, ingest_time) and is
// consumed by the traffic generator and the conformance probe (C3/C5/C6).
package envelope

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Client wraps a ModelService connection and fills the request envelope.
type Client struct {
	conn *grpc.ClientConn
	svc  servingv1.ModelServiceClient
}

// Dial connects to target (plaintext; model containers terminate no TLS) and
// waits until the connection is ready or timeout elapses. Every caller passes
// two arguments; extra exists so bufconn tests can add a WithContextDialer and
// run the readiness wait without a real port.
func Dial(target string, timeout time.Duration, extra ...grpc.DialOption) (*Client, error) {
	opts := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, extra...)
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", target, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn.Connect()
	for state := conn.GetState(); state != connectivity.Ready; state = conn.GetState() {
		if !conn.WaitForStateChange(ctx, state) {
			conn.Close()
			return nil, fmt.Errorf("dial %s: connection not ready within %v (state %v)", target, timeout, state)
		}
	}
	return &Client{conn: conn, svc: servingv1.NewModelServiceClient(conn)}, nil
}

// Close releases the underlying connection.
func (c *Client) Close() error { return c.conn.Close() }

// Health calls ModelService.Health.
func (c *Client) Health(ctx context.Context) (*servingv1.HealthResponse, error) {
	return c.svc.Health(ctx, &servingv1.HealthRequest{})
}

// Predict fills request_id (fresh v4 UUID) and ingest_time (now), sends the
// payload, and returns the response as-is: a non-OK Status is data for the
// caller (C3 asserts on it), not a Go error. The error return carries only
// transport/RPC failures.
func (c *Client) Predict(ctx context.Context, modelName, deviceID string, payload []byte) (*servingv1.PredictResponse, error) {
	id, err := newUUID()
	if err != nil {
		return nil, fmt.Errorf("generate request_id: %w", err)
	}
	return c.svc.Predict(ctx, &servingv1.PredictRequest{
		RequestId:  id,
		DeviceId:   deviceID,
		ModelName:  modelName,
		IngestTime: timestamppb.Now(),
		Payload:    payload,
	})
}

// newUUID returns a random (version 4, RFC 4122) UUID. Stdlib only; not worth
// a dependency for 8 lines.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
