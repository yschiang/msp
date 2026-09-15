"""MSP scientist SDK (MSP-SPEC-001 §4.5).

A scientist subclasses `BaseModel` and hands it to `serve()`; the SDK owns the
gRPC server, the envelope, manifest loading and health.
"""

from __future__ import annotations

from concurrent import futures

import grpc
from grpc_health.v1 import health, health_pb2, health_pb2_grpc

from msp._manifest import (
    DEFAULT_MANIFEST_PATH,
    load_input_message_class,
    load_manifest,
)
from msp._server import Servicer
from msp.serving.v1 import model_service_pb2_grpc as pb_grpc

__all__ = ["BaseModel", "serve"]


class BaseModel:
    """What a scientist implements. Both hooks run inside the model container."""

    def load(self) -> None:
        """Read weights and config; everything is inside the image (spec §4.3-1)."""

    def predict(self, payload: bytes) -> bytes:
        """Turn one serialized input message into one serialized output message."""
        raise NotImplementedError


def serve(
    model: BaseModel,
    manifest_path: str = DEFAULT_MANIFEST_PATH,
    port: int = 8080,
) -> None:
    """Serve `model` until the process is terminated.

    Startup order matters: the gRPC server starts first so both health
    surfaces (the envelope's Health and grpc.health.v1) answer not-ready while
    `load()` runs, which is what the platform's probes and conformance C2 poll
    against.
    """
    manifest = load_manifest(manifest_path)
    servicer = Servicer(model, manifest, load_input_message_class(manifest))

    # ponytail: fixed pool; make it a manifest field if a model ever needs more.
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=8))
    pb_grpc.add_ModelServiceServicer_to_server(servicer, server)
    # Standard grpc.health.v1 alongside the envelope's Health (Phase 1 D7):
    # Kubernetes' native gRPC probes can read only this one. Same server,
    # same port, same `ready` transition. HealthServicer() starts every
    # service as SERVING, so the not-ready state is set explicitly before
    # the server opens the port -- otherwise a probe could see SERVING
    # during load().
    health_servicer = health.HealthServicer()
    health_servicer.set("", health_pb2.HealthCheckResponse.NOT_SERVING)
    health_pb2_grpc.add_HealthServicer_to_server(health_servicer, server)
    server.add_insecure_port(f"[::]:{port}")

    server.start()  # non-blocking: gRPC serves from its own thread pool
    model.load()
    servicer.ready = True
    health_servicer.set("", health_pb2.HealthCheckResponse.SERVING)
    server.wait_for_termination()
