from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class ReadState(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    READ_STATE_UNSPECIFIED: _ClassVar[ReadState]
    READ_STATE_EXISTS: _ClassVar[ReadState]
    READ_STATE_NOT_FOUND: _ClassVar[ReadState]
READ_STATE_UNSPECIFIED: ReadState
READ_STATE_EXISTS: ReadState
READ_STATE_NOT_FOUND: ReadState

class ReadRequest(_message.Message):
    __slots__ = ("ids",)
    IDS_FIELD_NUMBER: _ClassVar[int]
    ids: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, ids: _Optional[_Iterable[str]] = ...) -> None: ...

class ReadResult(_message.Message):
    __slots__ = ("id", "state", "version", "data")
    ID_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    DATA_FIELD_NUMBER: _ClassVar[int]
    id: str
    state: ReadState
    version: int
    data: bytes
    def __init__(self, id: _Optional[str] = ..., state: _Optional[_Union[ReadState, str]] = ..., version: _Optional[int] = ..., data: _Optional[bytes] = ...) -> None: ...

class ReadResponse(_message.Message):
    __slots__ = ("results",)
    RESULTS_FIELD_NUMBER: _ClassVar[int]
    results: _containers.RepeatedCompositeFieldContainer[ReadResult]
    def __init__(self, results: _Optional[_Iterable[_Union[ReadResult, _Mapping]]] = ...) -> None: ...
