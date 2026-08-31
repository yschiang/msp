"""C2 negative fixture model: never becomes ready inside its startup budget.

Identical to contract/examples/defect-cls/model.py except that load() sleeps
600s before reading the config. Paired with a manifest declaring
startupSeconds: 10 (see Dockerfile.c2-slow-start).

This fails C2 rather than failing to start at all because msp.serve() calls
server.start() before model.load(): the gRPC server is listening and answering
Health with ready=false detail="model loading" the entire time. C2 is exactly
the check that a model reaches ready inside its declared budget.
"""

import time

import msp
import yaml
from msp.example.v1 import defect_pb2


class SlowStartDefectCls(msp.BaseModel):
    def load(self):
        time.sleep(600)
        with open("/opt/msp/config/params.yaml") as f:
            self.threshold = yaml.safe_load(f)["threshold"]

    def predict(self, payload: bytes) -> bytes:
        inp = defect_pb2.DefectInput.FromString(payload)
        score = sum(inp.features) / (len(inp.features) or 1)
        out = defect_pb2.DefectOutput(
            label="defect" if score > self.threshold else "ok",
            score=round(score, 6))
        return out.SerializeToString()


if __name__ == "__main__":
    msp.serve(SlowStartDefectCls())
