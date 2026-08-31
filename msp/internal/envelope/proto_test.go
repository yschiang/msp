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
// pinned byte-for-byte by the golden-bytes test below.
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

// goldenPredictResponse is the wire encoding of fixedPredictResponse(),
// captured the same way and for the same reason: PredictResponse carries the
// Status enum and inference_ms, both varint-encoded, so a renumbering or an
// int-type swap that a round trip would happily survive changes these bytes.
var goldenPredictResponse = []byte{
	0x0a, 0x06, 0x72, 0x65, 0x71, 0x2d, 0x34, 0x32, 0x12, 0x0a, 0x64, 0x65,
	0x66, 0x65, 0x63, 0x74, 0x2d, 0x63, 0x6c, 0x73, 0x1a, 0x02, 0x76, 0x34,
	0x22, 0x0f, 0x73, 0x68, 0x61, 0x32, 0x35, 0x36, 0x3a, 0x64, 0x65, 0x61,
	0x64, 0x62, 0x65, 0x65, 0x66, 0x2a, 0x02, 0xaa, 0xbb, 0x30, 0x02, 0x38,
	0xb9, 0x60,
}

func TestPredictResponseGoldenBytes(t *testing.T) {
	b, err := proto.Marshal(fixedPredictResponse())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, goldenPredictResponse) {
		t.Fatalf("wire encoding changed:\n got  %#v\n want %#v", b, goldenPredictResponse)
	}
}
