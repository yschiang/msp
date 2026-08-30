"""End-to-end tests for the scientist SDK, over a real gRPC channel.

Every test starts a real `msp.serve()` on a background thread and talks to it
with a generated stub -- no mocks, so the startup ordering and the per-request
status handling are exercised the way a model image runs them.
"""

from __future__ import annotations

import socket
import threading
import time

import grpc
import pytest
import yaml
from google.protobuf import descriptor_pb2

import msp
from msp.example.v1 import defect_pb2
from msp.serving.v1 import model_service_pb2 as pb
from msp.serving.v1 import model_service_pb2_grpc as pb_grpc

DIGEST = "sha256:abc"
MODEL_NAME = "defect-cls"
MODEL_VERSION = "v4.2.0"

VALID_PAYLOAD = defect_pb2.DefectInput(
    wafer_id="W-0001", features=[0.1, 0.2, 0.3]
).SerializeToString()
GARBAGE_PAYLOAD = b"\xff\xfe garbage"


class EchoModel(msp.BaseModel):
    """Minimal scientist model: parses DefectInput, returns a fixed DefectOutput."""

    def __init__(self) -> None:
        self.load_calls = 0
        self.predict_calls = 0
        self.fail = False

    def load(self) -> None:
        self.load_calls += 1

    def predict(self, payload: bytes) -> bytes:
        self.predict_calls += 1
        if self.fail:
            raise RuntimeError("boom")
        # Spend real time so inference_ms is a measurement the test can pin,
        # rather than a sub-millisecond value that floors to 0.
        time.sleep(0.005)
        parsed = defect_pb2.DefectInput()
        parsed.ParseFromString(payload)
        return defect_pb2.DefectOutput(label="defect", score=0.5).SerializeToString()


def _free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _write_manifest(tmp_dir) -> str:
    """Write a manifest plus the descriptor set it points at.

    The descriptor set is rebuilt from the committed `defect_pb2` gencode, so
    this test needs neither protoc nor the example model image's own
    `contract/examples/defect-cls/schemas/` directory.
    """
    descriptor = tmp_dir / "input.desc"
    file_set = descriptor_pb2.FileDescriptorSet()
    file_set.file.add().ParseFromString(defect_pb2.DESCRIPTOR.serialized_pb)
    descriptor.write_bytes(file_set.SerializeToString())

    manifest = tmp_dir / "model-manifest.yaml"
    manifest.write_text(
        yaml.safe_dump(
            {
                "apiVersion": "msp/v1",
                "kind": "ModelManifest",
                "model": {"name": MODEL_NAME, "version": MODEL_VERSION},
                "contract": {
                    "protocol": "grpc",
                    "port": 8080,
                    "inputSchema": {
                        "type": "protobuf",
                        "descriptor": str(descriptor),
                        "messageType": "msp.example.v1.DefectInput",
                    },
                    "outputSchema": {
                        "type": "protobuf",
                        "descriptor": str(descriptor),
                        "messageType": "msp.example.v1.DefectOutput",
                    },
                },
                "runtime": {"startupSeconds": 120},
                "comparisonPolicy": "exact",
                "goldenSamples": [
                    {
                        "input": "/opt/msp/golden/sample-01.bin",
                        "output": "/opt/msp/golden/expected-01.bin",
                    }
                ],
            }
        ),
        encoding="utf-8",
    )
    return str(manifest)


def _start(model: msp.BaseModel, manifest_path: str):
    """Run serve() on a daemon thread; return (stub, channel) once it accepts."""
    port = _free_port()
    thread = threading.Thread(
        target=msp.serve, args=(model, manifest_path, port), daemon=True
    )
    thread.start()
    channel = grpc.insecure_channel(f"127.0.0.1:{port}")
    # Connecting at all proves server.start() ran before model.load() finished.
    grpc.channel_ready_future(channel).result(timeout=10)
    return pb_grpc.ModelServiceStub(channel), channel


def _wait_ready(stub, timeout: float = 10.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if stub.Health(pb.HealthRequest()).ready:
            return
        time.sleep(0.02)
    raise AssertionError("server did not become ready within %ss" % timeout)


@pytest.fixture(scope="module")
def ready_server(tmp_path_factory):
    """One loaded server shared by the request-level tests."""
    manifest_path = _write_manifest(tmp_path_factory.mktemp("model"))
    model = EchoModel()
    # Read once at startup by the servicer, so it must be set before serve()
    # runs -- and restored afterwards, in case the dev box already set it.
    with pytest.MonkeyPatch.context() as patch:
        patch.setenv("MSP_MODEL_DIGEST", DIGEST)
        stub, channel = _start(model, manifest_path)
        _wait_ready(stub)
    yield stub, model
    channel.close()


def test_health_reports_ready(ready_server):
    stub, model = ready_server
    response = stub.Health(pb.HealthRequest())
    assert response.ready is True
    assert model.load_calls == 1


def test_valid_payload_returns_ok(ready_server):
    stub, _ = ready_server
    response = stub.Predict(
        pb.PredictRequest(request_id="req-1", payload=VALID_PAYLOAD)
    )
    assert response.status == pb.OK
    assert response.request_id == "req-1"
    assert response.model_name == MODEL_NAME
    assert response.model_version == MODEL_VERSION
    # predict() sleeps 5ms, so anything under that is not a real measurement.
    assert response.inference_ms >= 5
    output = defect_pb2.DefectOutput()
    output.ParseFromString(response.payload)
    assert output.label == "defect"
    assert output.score == pytest.approx(0.5)


def test_unparsable_payload_is_invalid_input_and_skips_predict(ready_server):
    stub, model = ready_server
    before = model.predict_calls
    response = stub.Predict(
        pb.PredictRequest(request_id="req-2", payload=GARBAGE_PAYLOAD)
    )
    assert response.status == pb.INVALID_INPUT
    assert response.request_id == "req-2"
    assert model.predict_calls == before


def test_predict_raising_is_internal_error(tmp_path):
    # Its own server: a failing model must not be shared with the other tests.
    model = EchoModel()
    model.fail = True
    stub, channel = _start(model, _write_manifest(tmp_path))
    try:
        _wait_ready(stub)
        response = stub.Predict(
            pb.PredictRequest(request_id="req-3", payload=VALID_PAYLOAD)
        )
    finally:
        channel.close()
    assert response.status == pb.INTERNAL_ERROR
    assert response.request_id == "req-3"
    assert model.predict_calls == 1


def test_model_digest_comes_from_env(ready_server):
    stub, _ = ready_server
    response = stub.Predict(
        pb.PredictRequest(request_id="req-4", payload=VALID_PAYLOAD)
    )
    assert response.model_digest == DIGEST


def test_not_ready_until_load_returns(tmp_path):
    """Health serves before load() finishes, and Predict refuses until it does."""
    release = threading.Event()

    class SlowModel(EchoModel):
        def load(self) -> None:
            release.wait(10)
            super().load()

    model = SlowModel()
    stub, channel = _start(model, _write_manifest(tmp_path))
    try:
        health = stub.Health(pb.HealthRequest())
        assert health.ready is False
        assert health.detail == "model loading"

        response = stub.Predict(
            pb.PredictRequest(request_id="req-5", payload=VALID_PAYLOAD)
        )
        assert response.status == pb.INTERNAL_ERROR
        assert response.inference_ms == 0  # nothing was measured, nothing ran
        assert model.predict_calls == 0

        release.set()
        _wait_ready(stub)
    finally:
        release.set()
        channel.close()
