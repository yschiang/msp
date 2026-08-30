"""defect-cls: Phase 0 stand-in model (MSP-SPEC-001 §4.5 scientist contract).

CPU-only average-vs-threshold classifier. No randomness, no wall-clock reads,
no data-dependent iteration order -- `sum()` over a Python list is a fixed
left-to-right fold, so the same input always produces the same float bit
pattern on any machine (IEEE-754 double arithmetic is deterministic for a
fixed operation order). That determinism is what lets goldenSamples use
comparisonPolicy: exact.
"""

import msp
import yaml
from msp.example.v1 import defect_pb2


class DefectCls(msp.BaseModel):
    def load(self):
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
    msp.serve(DefectCls())
