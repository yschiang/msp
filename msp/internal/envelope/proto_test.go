package envelope

import (
	"bytes"
	"testing"
	"time"

	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fixedPredictRequest is fully populated (every field set to a non-default
// value, one metadata entry so map serialization stays deterministic) and is
// shared by the round-trip test and the golden-bytes test below.
func fixedPredictRequest() *servingv1.PredictRequest {
	return &servingv1.PredictRequest{
		RequestId:  "req-42",
		DeviceId:   "dev-7",
		ModelName:  "defect-cls",
		IngestTime: timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		Payload:    []byte{0x01, 0x02, 0x03},
		Metadata:   map[string]string{"trace_id": "t-1"},
	}
}

func fixedPredictResponse() *servingv1.PredictResponse {
	return &servingv1.PredictResponse{
		RequestId:    "req-42",
		ModelName:    "defect-cls",
		ModelVersion: "v4",
		ModelDigest:  "sha256:deadbeef",
		Payload:      []byte{0xaa, 0xbb},
		Status:       servingv1.Status_INTERNAL_ERROR,
		InferenceMs:  12345,
	}
}

// TestPredictRequestRoundTrip marshals and unmarshals a PredictRequest with
// every field set (including the embedded Timestamp and the map) and checks
// each field survives the round trip.
func TestPredictRequestRoundTrip(t *testing.T) {
	req := fixedPredictRequest()
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got servingv1.PredictRequest
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.RequestId != req.RequestId {
		t.Errorf("RequestId = %q, want %q", got.RequestId, req.RequestId)
	}
	if got.DeviceId != req.DeviceId {
		t.Errorf("DeviceId = %q, want %q", got.DeviceId, req.DeviceId)
	}
	if got.ModelName != req.ModelName {
		t.Errorf("ModelName = %q, want %q", got.ModelName, req.ModelName)
	}
	if !got.GetIngestTime().AsTime().Equal(req.GetIngestTime().AsTime()) {
		t.Errorf("IngestTime = %v, want %v", got.GetIngestTime().AsTime(), req.GetIngestTime().AsTime())
	}
	if !bytes.Equal(got.Payload, req.Payload) {
		t.Errorf("Payload = %v, want %v", got.Payload, req.Payload)
	}
	if len(got.Metadata) != len(req.Metadata) || got.Metadata["trace_id"] != req.Metadata["trace_id"] {
		t.Errorf("Metadata = %v, want %v", got.Metadata, req.Metadata)
	}
}

// TestPredictResponseRoundTrip marshals and unmarshals a PredictResponse with
// every field set (including the Status enum and the int64 inference_ms) and
// checks each field survives the round trip.
func TestPredictResponseRoundTrip(t *testing.T) {
	resp := fixedPredictResponse()
	b, err := proto.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var got servingv1.PredictResponse
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.RequestId != resp.RequestId {
		t.Errorf("RequestId = %q, want %q", got.RequestId, resp.RequestId)
	}
	if got.ModelName != resp.ModelName {
		t.Errorf("ModelName = %q, want %q", got.ModelName, resp.ModelName)
	}
	if got.ModelVersion != resp.ModelVersion {
		t.Errorf("ModelVersion = %q, want %q", got.ModelVersion, resp.ModelVersion)
	}
	if got.ModelDigest != resp.ModelDigest {
		t.Errorf("ModelDigest = %q, want %q", got.ModelDigest, resp.ModelDigest)
	}
	if !bytes.Equal(got.Payload, resp.Payload) {
		t.Errorf("Payload = %v, want %v", got.Payload, resp.Payload)
	}
	if got.Status != resp.Status {
		t.Errorf("Status = %v, want %v", got.Status, resp.Status)
	}
	if got.InferenceMs != resp.InferenceMs {
		t.Errorf("InferenceMs = %d, want %d", got.InferenceMs, resp.InferenceMs)
	}
}

// goldenPredictRequest is the wire encoding of fixedPredictRequest(), captured
// once with `proto.Marshal`. Any change to a field number or wire type in
// PredictRequest changes these bytes, so this test fails loudly even when a
// plain round trip would still pass (e.g. renumbering a field, or swapping a
// varint-compatible int type). The message has a single metadata entry so map
// serialization order does not make this test flaky.
var goldenPredictRequest = []byte{
	0x0a, 0x06, 0x72, 0x65, 0x71, 0x2d, 0x34, 0x32, 0x12, 0x05, 0x64, 0x65,
	0x76, 0x2d, 0x37, 0x1a, 0x0a, 0x64, 0x65, 0x66, 0x65, 0x63, 0x74, 0x2d,
	0x63, 0x6c, 0x73, 0x22, 0x06, 0x08, 0xa5, 0xeb, 0xdc, 0xca, 0x06, 0x2a,
	0x03, 0x01, 0x02, 0x03, 0x32, 0x0f, 0x0a, 0x08, 0x74, 0x72, 0x61, 0x63,
	0x65, 0x5f, 0x69, 0x64, 0x12, 0x03, 0x74, 0x2d, 0x31,
}

func TestPredictRequestGoldenBytes(t *testing.T) {
	b, err := proto.Marshal(fixedPredictRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, goldenPredictRequest) {
		t.Fatalf("wire encoding changed:\n got  %#v\n want %#v", b, goldenPredictRequest)
	}
}
