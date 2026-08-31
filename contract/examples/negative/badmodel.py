"""C3 negative fixture model: correct answers wrapped in a broken envelope.

A raw grpcio server, NOT msp.serve(). That is the whole point of the fixture:
the SDK's Servicer parses every payload against the manifest's input message
before calling predict() and returns INVALID_INPUT when that parse fails, so a
model built on the SDK physically cannot answer OK to a wire-corrupt payload.
To break C3 the fixture has to own the servicer.

It still needs the generated protobuf modules. Those ship inside the installed
`msp` SDK package on their natural proto package paths (msp/serving/v1/,
msp/example/v1/ -- see docs/SCAFFOLDING.md "Codegen rules"), so importing
`msp.serving.v1.model_service_pb2` gets the codegen and nothing else. No
validation is inherited: it lives in msp._server.Servicer, which this file
never touches.

Two envelope violations, both of them things C3 explicitly asserts (spec
§4.1/§4.3, conformance §4.4 C3):

  1. model_version is empty, where the manifest declares "v1";
  2. a payload that does not parse is answered OK instead of INVALID_INPUT.

Everything else is honest: golden inputs get exactly the answer the real
example model gives, computed the same way from the same config, so C4, C5 and
C6 all still pass and this fixture breaks C3 alone.
"""

from concurrent import futures

import grpc
import yaml
from msp.example.v1 import defect_pb2
from msp.serving.v1 import model_service_pb2 as pb
from msp.serving.v1 import model_service_pb2_grpc as pb_grpc

MODEL_NAME = "defect-cls"
PORT = 8080


class BadEnvelopeServicer(pb_grpc.ModelServiceServicer):
    def __init__(self, threshold: float) -> None:
        self._threshold = threshold

    def Health(self, request, context):
        return pb.HealthResponse(ready=True)

    def Predict(self, request, context):
        response = pb.PredictResponse(
            request_id=request.request_id,
            model_name=MODEL_NAME,
            model_version="",  # violation 1: the manifest declares "v1"
            status=pb.OK,      # violation 2: OK even when the payload is garbage
        )
        try:
            inp = defect_pb2.DefectInput.FromString(request.payload)
        except Exception:
            return response  # OK with an empty payload, never INVALID_INPUT
        score = sum(inp.features) / (len(inp.features) or 1)
        response.payload = defect_pb2.DefectOutput(
            label="defect" if score > self._threshold else "ok",
            score=round(score, 6)).SerializeToString()
        return response


def main() -> None:
    with open("/opt/msp/config/params.yaml") as f:
        threshold = yaml.safe_load(f)["threshold"]
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=8))
    pb_grpc.add_ModelServiceServicer_to_server(BadEnvelopeServicer(threshold), server)
    server.add_insecure_port(f"[::]:{PORT}")
    server.start()
    server.wait_for_termination()


if __name__ == "__main__":
    main()
