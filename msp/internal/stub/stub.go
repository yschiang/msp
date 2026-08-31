// Package stub implements a canned-response ModelService gRPC server. It is
// the Phase 0 stand-in for the real Router (Phase 2): MYSVC and the traffic
// generator (Task 7) integrate against it before the real Router exists, and
// the conformance probe (Task 8) unit-tests its checks against it over
// bufconn, with the misbehavior hooks below, no docker needed
// (MSP-SPEC-001 §11 Phase 0).
package stub

import (
	"context"
	"fmt"
	"os"

	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
)

// DefaultVersion is the model_version every Predict response carries unless a
// test overrides Server.Version.
const DefaultVersion = "stub-v0"

// Server implements servingv1.ModelServiceServer with a canned response:
// Predict validates the envelope and otherwise always succeeds; Health is
// always ready. Version and PayloadFunc are exported so Task 8's conformance
// probe can construct a Server directly and drive misbehavior (wrong version,
// non-deterministic payload) from a test, without a CLI flag exposing either.
type Server struct {
	servingv1.UnimplementedModelServiceServer

	// Version is the model_version returned in every Predict response.
	// NewServer sets it to DefaultVersion; tests override it after
	// construction to simulate a model reporting the wrong version.
	Version string

	// PayloadFunc, if set, overrides the response payload on every Predict
	// call. It receives the payload the stub would otherwise return (the
	// response-file bytes, or the request payload echoed back) and returns
	// the payload to send instead. Tests use it to inject a non-deterministic
	// payload (e.g. a fresh random value each call).
	PayloadFunc func(base []byte) []byte

	// responsePayload is the response-file bytes read once at startup, or nil
	// to echo each request's payload.
	responsePayload []byte
}

// NewServer builds a Server. If responseFile is non-empty, its bytes are read
// once here and returned as the payload for every Predict call; an unreadable
// file fails loudly at construction rather than turning every request into an
// error. An empty responseFile means echo mode: each response's payload is
// that request's payload.
func NewServer(responseFile string) (*Server, error) {
	s := &Server{Version: DefaultVersion}
	if responseFile != "" {
		b, err := os.ReadFile(responseFile)
		if err != nil {
			return nil, fmt.Errorf("read response file %s: %w", responseFile, err)
		}
		s.responsePayload = b
	}
	return s, nil
}

// Predict validates the envelope (non-empty request_id, model_name, payload;
// otherwise INVALID_INPUT as response data, not a gRPC error) and returns the
// canned response.
func (s *Server) Predict(_ context.Context, req *servingv1.PredictRequest) (*servingv1.PredictResponse, error) {
	if req.RequestId == "" || req.ModelName == "" || len(req.Payload) == 0 {
		return &servingv1.PredictResponse{
			RequestId: req.RequestId,
			ModelName: req.ModelName,
			Status:    servingv1.Status_INVALID_INPUT,
		}, nil
	}

	payload := req.Payload
	if s.responsePayload != nil {
		payload = s.responsePayload
	}
	if s.PayloadFunc != nil {
		payload = s.PayloadFunc(payload)
	}

	return &servingv1.PredictResponse{
		RequestId:    req.RequestId,
		ModelName:    req.ModelName,
		ModelVersion: s.Version,
		ModelDigest:  "",
		Payload:      payload,
		Status:       servingv1.Status_OK,
	}, nil
}

// Health always reports ready: the stub has nothing to load.
func (s *Server) Health(context.Context, *servingv1.HealthRequest) (*servingv1.HealthResponse, error) {
	return &servingv1.HealthResponse{Ready: true}, nil
}
