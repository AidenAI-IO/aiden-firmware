# Unix Socket Protocol

This project uses Unix domain sockets for hardware services that must run in C++
and be consumed by other processes.

## Message Envelope

Each message is a single frame:

1. `uint32` little-endian JSON header length
2. `uint64` little-endian binary payload length
3. UTF-8 JSON header bytes
4. Optional binary payload bytes

The JSON header describes the request or response. Large binary data, such as a
raw HDMI frame, is carried in the payload instead of base64-encoding it into JSON.

## C++ Transport Layer

- `src/uds_message.*` implements envelope read/write.
- `src/uds_client.*` implements one-shot request/response client calls.
- `src/uds_server.*` implements socket bind/listen/accept/thread lifecycle and
  dispatches each parsed request to a service-specific handler.
- `src/frame_ipc.*` remains as a compatibility wrapper for existing frame-service
  tests and callers.

## Frame Service Usage

`frame_service` is one service built on the generic UDS transport. Its domain
protocol remains unchanged:

- `health`
- `latest_frame`
- `get_frame`
- `list_frames`
- `restart`

The default frame service socket is `/tmp/frame_service.sock`.

## Cross-Language Clients

Go or other language clients do not need C++ ABI or cgo. They only need to:

1. connect to the service socket,
2. write the 12-byte envelope prefix,
3. write the JSON header and optional payload,
4. read the response envelope the same way.

Keep large integer fields that may exceed JavaScript's safe integer range encoded
as JSON strings, matching the current frame-service protocol.
