# aonohako SSE Protocol

Both `/compile` and `/execute` open SSE streams and terminate with exactly
one `result` event.

When `aonohako` is configured with remote execution transport, the local server keeps the same SSE contract for `/compile` and `/execute`. `/execute` forwards `log`, `image`, `error`, and `result` from the remote runner. `/compile` returns the remote compile result with the same buffered `log` and `result` shape.

## Event Types

| Event | Emitted by | Description |
|---|---|---|
| `progress` | both | Acceptance, queue position, and start notifications |
| `image` | `/execute` | Real-time image frames from sidecar output |
| `log` | both | buffered stdout / stderr payloads emitted before `result` |
| `result` | both | **Final** response (exactly once per request) |
| `error` | both | Terminal error (emitted **before** `result` on failure) |
| `heartbeat` | both | Periodic keep-alive |

---

## `/compile` Event Flow

```
Client                        aonohako
  |--- POST /compile --------->|
  |                            |  (if shared queue is full → 429)
  |<-- progress (accepted) ----|
  |<-- log (buffered stderr) --|
  |<-- result (CompileResp) ---|
```

### `progress`

```json
{"stage": "accepted", "request_id": "compile-1", "queue_pending": 0}
```

### `result` (CompileResponse)

```jsonc
{
  "status": "OK",                        // "OK" | "Compile Error" | "Timeout" | "Invalid Request" | "Internal Error"
  "artifacts": [
    {
      "name": "Main",                    // artifact filename
      "data_b64": "<base64>",            // base64-encoded binary/bytecode
      "mode": "exec"                     // "exec" for executables, "" for data
    }
  ],
  "stdout": "",                          // compiler stdout, capped at 1 MiB
  "stderr": "",                          // compiler stderr, capped at 1 MiB
  "stdout_truncated": false,             // true when stdout exceeded the compile capture cap
  "stderr_truncated": false,             // true when stderr exceeded the compile capture cap
  "reason": ""                           // human-readable failure reason
}
```

---

## `/execute` Event Flow

```
Client                        aonohako
  |--- POST /execute --------->|
  |                            |  (if shared queue is full → 429)
  |<-- progress (accepted) ----|
  |        ...waiting...       |  (queued until slot available)
  |<-- progress (start) -------|
  |<-- image ------------------|  (if sidecar image output)
  |<-- log (buffered stdout/stderr) --|
  |<-- result (RunResponse) ---|
```

### `progress`

```json
{"stage": "accepted", "request_id": "execute-1", "queue_position": 0, "active_runs": 1, "queue_pending": 0, "ts": 1700000000000}
{"stage": "start", "request_id": "execute-1", "ts": 1700000000100}
```

### `image`

```json
{"mime": "image/png", "b64": "<base64>", "ts": 1700000000500}
```

### `log`

```json
{"stream": "stdout", "chunk": "hello world\n"}
{"stream": "stderr", "chunk": "warning: ..."}
```

### `result` (RunResponse)

```jsonc
{
  "status": "Accepted",                 // see Status Codes below
  "time_ms": 42,                        // compatibility alias for wall_time_ms
  "wall_time_ms": 42,                   // wall-clock execution time (ms)
  "cpu_time_ms": 17,                    // CPU time from process CPU clock (ms)
  "memory_kb": 8192,                    // peak RSS (KB, from getrusage)
  "exit_code": 0,                       // nullable; process exit code
  "stdout": "",                         // truncated stdout (up to `limits.output_bytes`; default `64 KiB`, hard cap `8 MiB`)
  "stderr": "",                         // truncated stderr (up to `limits.output_bytes`; on non-zero exit)
  "stdout_truncated": false,            // true when stdout exceeded the capture cap
  "stderr_truncated": false,            // true when stderr exceeded the capture cap
  "reason": "",                         // failure reason
  "score": null,                        // nullable float; SPJ score (0.0–1.0)
  "sidecar_outputs": [                  // captured sidecar files
    {"path": "result.txt", "data_b64": "<base64>"}
  ],
  "sidecar_errors": [                   // optional rejected sidecar diagnostics
    {"path": "debug.txt", "reason": "file too large"}
  ]
}
```

### Status Codes

| Status | Meaning |
|---|---|
| `Accepted` | Output matches expected |
| `Wrong Answer` | Output does not match |
| `Time Limit Exceeded` | Execution exceeded time limit |
| `Memory Limit Exceeded` | Peak RSS exceeded memory limit |
| `Workspace Limit Exceeded` | Workspace file growth exceeded `limits.workspace_bytes` |
| `Runtime Error` | Non-zero exit or signal |
| `Container Initialization Failed` | Workspace setup or process start failed |

---

## Queue & Rate Limiting

Both `/compile` and `/execute` share the same bounded queue:

- `/compile` rejects missing sources, more than 512 sources, source files over
  16 MiB decoded, source totals over 48 MiB decoded, and invalid or unknown
  `runtime_profile` values before acquiring a stream or queue slot.
- `/execute` rejects oversized `stdin` / `expected_stdout`, out-of-range run
  limits, invalid or unknown `runtime_profile` values, and disallowed
  `enable_network=true` before acquiring a stream or queue slot.
- **Active slots**: `AONOHAKO_MAX_ACTIVE_RUNS` (default: `1` for
  `embedded + helper`, also `1` in `AONOHAKO_DEPLOYMENT_TARGET=cloudrun`,
  otherwise `max(1, cpu−2)`). The `embedded + helper` backend rejects values
  other than `1`.
- **Pending queue**: `AONOHAKO_MAX_PENDING_QUEUE` (default: `16`; set `0`
  explicitly only for an unlimited development queue)
- **Open request streams**: `AONOHAKO_MAX_ACTIVE_STREAMS` (default: `64`; set
  `0` explicitly only for unlimited development streams). This caps
  simultaneous `/compile` and `/execute` streams before they join the run queue.
- **Per-principal open request streams**:
  `AONOHAKO_MAX_PRINCIPAL_ACTIVE_STREAMS` (default: `0` in `dev`, `16` in
  `cloudrun` or `selfhosted`). This caps simultaneous streams for one bearer,
  platform, or anonymous remote principal.
- **Per-principal request rate**:
  `AONOHAKO_MAX_PRINCIPAL_REQUESTS_PER_MINUTE` (default: `0` in `dev`, `60` in
  `cloudrun` or `selfhosted`). This caps `/compile` and `/execute` requests per
  fixed one-minute window for one bearer, platform, or anonymous remote
  principal. Stale per-principal windows are cleaned up after they age out so
  high-cardinality principal traffic does not retain rate state indefinitely.
- **Request body read timeout**: `AONOHAKO_BODY_READ_TIMEOUT_SEC` (default:
  `30`). This bounds the HTTP request-body upload window before the response
  switches to SSE streaming.
- **Remote SSE idle timeout**: `AONOHAKO_REMOTE_SSE_IDLE_TIMEOUT_SEC` (default:
  `30`). This bounds how long a remote `/compile` or `/execute` stream may stay
  silent before the control plane cancels it.

Numeric queue/timing environment variables are strict: malformed values,
negative values, or zero values where a positive integer is required fail server
startup instead of silently falling back.

When the active stream cap is reached, the server returns:

```
HTTP/1.1 429 Too Many Requests
Content-Type: application/json

{"error": "stream_limit_exceeded"}
```

When the per-principal stream cap is reached, the server returns:

```
HTTP/1.1 429 Too Many Requests
Content-Type: application/json

{"error": "principal_stream_limit_exceeded"}
```

When the per-principal request-rate cap is reached, the server returns:

```
HTTP/1.1 429 Too Many Requests
Content-Type: application/json

{"error": "principal_rate_limited"}
```

When the pending queue is full, the server returns:

```
HTTP/1.1 429 Too Many Requests
Content-Type: application/json

{"error": "queue_full"}
```

Callers should implement exponential backoff on 429.

---

## HTTP Headers

| Header | Value |
|---|---|
| `Content-Type` | `text/event-stream` |
| `Cache-Control` | `no-cache` |
| `Connection` | `keep-alive` |
| `X-Accel-Buffering` | `no` |
| `X-Aonohako-Protocol-Version` | `2026-04-24` |

Remote control planes accept missing protocol-version headers for older
runners, but reject a present `X-Aonohako-Protocol-Version` value that does not
match the current protocol.
