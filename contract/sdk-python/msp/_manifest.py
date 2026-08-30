"""Manifest and input-schema loading for the scientist SDK.

Minimal field access only. Validating the manifest against
`contract/manifest.schema.json` is the conformance harness's job (spec §4.4 C1),
not the model container's -- a model that reaches production has already passed
C1, so re-validating at startup only slows the container down.
"""

from __future__ import annotations

from typing import Any

import yaml
from google.protobuf import descriptor_pb2, descriptor_pool, message_factory

# Fixed by spec §4.2/§4.5: every model image declares its manifest here.
DEFAULT_MANIFEST_PATH = "/opt/msp/model-manifest.yaml"


def load_manifest(path: str) -> dict[str, Any]:
    with open(path, "r", encoding="utf-8") as handle:
        return yaml.safe_load(handle)


def load_input_message_class(manifest: dict[str, Any]):
    """Return the generated message class for `contract.inputSchema`.

    The manifest points at a serialized `FileDescriptorSet` (built with
    `protoc --include_imports`, so it carries its own dependencies) inside the
    image; it is loaded into a private descriptor pool to avoid colliding with
    anything already registered in the default pool.
    """
    schema = manifest["contract"]["inputSchema"]
    with open(schema["descriptor"], "rb") as handle:
        file_set = descriptor_pb2.FileDescriptorSet.FromString(handle.read())

    pool = descriptor_pool.DescriptorPool()
    for file_proto in file_set.file:
        pool.Add(file_proto)
    return message_factory.GetMessageClass(
        pool.FindMessageTypeByName(schema["messageType"])
    )
