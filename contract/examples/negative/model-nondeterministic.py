"""C6 negative fixture model: a different answer every call.

Identical to contract/examples/defect-cls/model.py except that the score is
random.random() instead of the mean of the input features. The output is still
a well-formed DefectOutput, so it parses cleanly (C3 and C4 stay clean); it
simply never agrees with itself.

Deliberately unseeded: C6 sends the same golden input three times on one
connection and compares the three responses pairwise under comparisonPolicy
exact. DefectOutput.score is a proto `float`, so two draws collide only if they
round to the same float32 -- on the order of 1 in 10 million per pair, over
three pairs. This fixture fails C6 on every practical run; it is not a flaky
test that merely tends to go red.
"""

import random

import msp
import yaml
from msp.example.v1 import defect_pb2


class NondeterministicDefectCls(msp.BaseModel):
    def load(self):
        with open("/opt/msp/config/params.yaml") as f:
            self.threshold = yaml.safe_load(f)["threshold"]

    def predict(self, payload: bytes) -> bytes:
        defect_pb2.DefectInput.FromString(payload)  # parse, then ignore
        score = random.random()
        out = defect_pb2.DefectOutput(
            label="defect" if score > self.threshold else "ok",
            score=score)
        return out.SerializeToString()


if __name__ == "__main__":
    msp.serve(NondeterministicDefectCls())
