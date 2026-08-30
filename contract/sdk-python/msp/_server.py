"""gRPC servicer implementing the msp.serving.v1 envelope (spec §4.1, §4.3)."""

from __future__ import annotations

import os
import time
import traceback
from typing import Any

from msp.serving.v1 import model_service_pb2 as pb
from msp.serving.v1 import model_service_pb2_grpc as pb_grpc

LOADING_DETAIL = "model loading"


class Servicer(pb_grpc.ModelServiceServicer):
    """Serves one model. `ready` is flipped by serve() once load() returns."""

    def __init__(self, model, manifest: dict[str, Any], input_class) -> None:
        self._model = model
        self._input_class = input_class
        model_section = manifest.get("model") or {}
        self._model_name = model_section.get("name", "")
        self._model_version = model_section.get("version", "")
        # SPEC-GAP stand-in for §4.1 "digest read from the manifest at startup":
        # an image cannot contain its own digest, so the platform injects it.
        self._model_digest = os.environ.get("MSP_MODEL_DIGEST", "")
        self.ready = False

    def Health(self, request, context):
        return pb.HealthResponse(
            ready=self.ready, detail="" if self.ready else LOADING_DETAIL
        )

    def Predict(self, request, context):
        response = pb.PredictResponse(
            request_id=request.request_id,
            model_name=self._model_name,
            model_version=self._model_version,
            model_digest=self._model_digest,
        )

        if not self.ready:
            response.status = pb.INTERNAL_ERROR
            return response

        try:
            self._input_class().ParseFromString(request.payload)
        except Exception:
            # ponytail: parse-failure rejection only; unknown-field strictness
            # needs UnknownFields() walk, add if schema drift alerting demands it
            response.status = pb.INVALID_INPUT
            return response

        started = time.perf_counter()
        try:
            # Assigning inside the try also catches a predict() that returns
            # something other than bytes.
            response.payload = self._model.predict(request.payload)
            response.status = pb.OK
        except Exception:
            # The envelope has no error detail field, so stderr is the only
            # place an operator can see why a model blew up.
            traceback.print_exc()
            response.status = pb.INTERNAL_ERROR
        response.inference_ms = int((time.perf_counter() - started) * 1000)
        return response
