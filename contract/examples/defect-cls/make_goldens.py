"""Generate golden/*.bin by running DefectCls.predict() directly (no gRPC).

Run from the repo root with the SDK on PYTHONPATH:

    PYTHONPATH=contract/sdk-python python3 contract/examples/defect-cls/make_goldens.py

Threshold is read from the checked-in config/params.yaml -- the same value
model.py reads from /opt/msp/config/params.yaml inside the container -- so
generation exercises the real predict() logic without needing that container
path to exist on the host.
"""

import pathlib
import sys

import yaml

HERE = pathlib.Path(__file__).parent
sys.path.insert(0, str(HERE))  # so `import model` finds model.py next to this script

from model import DefectCls  # noqa: E402
from msp.example.v1 import defect_pb2  # noqa: E402

# avg > 0.5 -> "defect"; avg < 0.5 -> "ok". Uniform features so the average is
# the value itself (no division rounding) and both are exact in float32/float64,
# well clear of the 0.5 threshold.
SAMPLES = [
    ("sample-01", "expected-01", "W-0001", [0.75, 0.75, 0.75]),
    ("sample-02", "expected-02", "W-0002", [0.25, 0.25, 0.25]),
]


def main() -> None:
    model = DefectCls()
    with open(HERE / "config" / "params.yaml") as f:
        model.threshold = yaml.safe_load(f)["threshold"]

    golden_dir = HERE / "golden"
    golden_dir.mkdir(exist_ok=True)

    for input_name, output_name, wafer_id, features in SAMPLES:
        payload = defect_pb2.DefectInput(
            wafer_id=wafer_id, features=features
        ).SerializeToString()
        (golden_dir / f"{input_name}.bin").write_bytes(payload)

        expected = model.predict(payload)
        (golden_dir / f"{output_name}.bin").write_bytes(expected)
        out = defect_pb2.DefectOutput.FromString(expected)
        print(f"{input_name}: features={features} -> label={out.label!r} score={out.score}")


if __name__ == "__main__":
    main()
