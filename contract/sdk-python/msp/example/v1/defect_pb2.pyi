from google.protobuf.internal import containers as _containers
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable
from typing import ClassVar as _ClassVar, Optional as _Optional

DESCRIPTOR: _descriptor.FileDescriptor

class DefectInput(_message.Message):
    __slots__ = ("wafer_id", "features")
    WAFER_ID_FIELD_NUMBER: _ClassVar[int]
    FEATURES_FIELD_NUMBER: _ClassVar[int]
    wafer_id: str
    features: _containers.RepeatedScalarFieldContainer[float]
    def __init__(self, wafer_id: _Optional[str] = ..., features: _Optional[_Iterable[float]] = ...) -> None: ...

class DefectOutput(_message.Message):
    __slots__ = ("label", "score")
    LABEL_FIELD_NUMBER: _ClassVar[int]
    SCORE_FIELD_NUMBER: _ClassVar[int]
    label: str
    score: float
    def __init__(self, label: _Optional[str] = ..., score: _Optional[float] = ...) -> None: ...
