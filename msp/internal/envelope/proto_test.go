package envelope

import (
	"testing"

	servingv1 "github.com/yschiang/msp/msp/gen/servingv1"
	"google.golang.org/protobuf/proto"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	req := &servingv1.PredictRequest{RequestId: "r1", DeviceId: "d1", ModelName: "defect-cls"}
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got servingv1.PredictRequest
	if err := proto.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.RequestId != "r1" || got.ModelName != "defect-cls" {
		t.Fatalf("round trip mismatch: %+v", &got)
	}
}
