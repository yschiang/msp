"""Smoke test for the built msp-example-defect-cls:dev image.

Runs the real image via `docker run`, sends the golden sample-01 input over a
real gRPC channel, and checks the response payload is byte-identical to
expected-01.bin (comparisonPolicy: exact). Skips (not fails) when docker is
unavailable, so this test doesn't gate environments without it.

Run from repo root:
    PYTHONPATH=contract/sdk-python python3 -m pytest contract/examples/defect-cls/tests -v
"""

from __future__ import annotations

import pathlib
import shutil
import subprocess
import time

import grpc
import pytest

from msp.serving.v1 import model_service_pb2 as pb
from msp.serving.v1 import model_service_pb2_grpc as pb_grpc

IMAGE = "msp-example-defect-cls:dev"
PORT = 18080
GOLDEN_DIR = pathlib.Path(__file__).parent.parent / "golden"
STARTUP_TIMEOUT_S = 30  # manifest runtime.startupSeconds


def _docker_available() -> bool:
    if shutil.which("docker") is None:
        return False
    try:
        return subprocess.run(
            ["docker", "info"], capture_output=True, timeout=10
        ).returncode == 0
    except (subprocess.TimeoutExpired, OSError):
        # This runs at module import, inside the pytestmark expression. An
        # unhandled exception here is a pytest COLLECTION ERROR, not a skip --
        # a hung docker daemon would fail the suite instead of stepping over it.
        return False


pytestmark = pytest.mark.skipif(
    not _docker_available(), reason="docker not available"
)


@pytest.fixture
def running_container():
    container_id = subprocess.run(
        ["docker", "run", "--rm", "-d", "-p", f"{PORT}:8080", IMAGE],
        capture_output=True, text=True, check=True,
    ).stdout.strip()
    try:
        yield
    finally:
        subprocess.run(["docker", "stop", container_id], capture_output=True)


def _wait_ready(stub: pb_grpc.ModelServiceStub, deadline: float) -> pb.HealthResponse:
    last = None
    while time.monotonic() < deadline:
        try:
            last = stub.Health(pb.HealthRequest(), timeout=2)
            if last.ready:
                return last
        except grpc.RpcError:
            pass
        time.sleep(0.2)
    raise TimeoutError(f"model never became ready; last Health response: {last}")


def test_predict_matches_golden_sample_01(running_container):
    with grpc.insecure_channel(f"localhost:{PORT}") as channel:
        stub = pb_grpc.ModelServiceStub(channel)
        health = _wait_ready(stub, time.monotonic() + STARTUP_TIMEOUT_S)
        assert health.ready is True

        sample_01 = (GOLDEN_DIR / "sample-01.bin").read_bytes()
        expected_01 = (GOLDEN_DIR / "expected-01.bin").read_bytes()

        response = stub.Predict(
            pb.PredictRequest(request_id="smoke-test-01", payload=sample_01)
        )

        assert response.status == pb.OK
        assert response.payload == expected_01
