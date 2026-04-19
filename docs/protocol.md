# aonohako SSE Protocol

Both `/compile` and `/execute` open SSE streams and terminate with exactly
one `result` event.

## Event Types

| Event | Emitted by | Description |
|---|---|---|
| `progress` | both | Acceptance, queue position, and start notifications |
| `image` | `/execute` | Real-time image frames from sidecar output |
| `log` | both | stdout / stderr chunks |
| `result` | both | **Final** response (exactly once per request) |
| `error` | both | Terminal error (emitted **before** `result` on failure) |
| `heartbeat` | both | Periodic keep-alive |

---

## `/compile` Event Flow

```
Client                        aonohako
  |--- POST /compile --------->|
  |<-- progress (accepted) ----|
  |<-- log (stderr chunk) -----|   (during compilation)
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
  "stdout": "",                          // compiler stdout
  "stderr": "",                          // compiler stderr
  "reason": ""                           // human-readable failure reason
}
```

---

## `/execute` Event Flow

```
Client                        aonohako
  |--- POST /execute --------->|
  |                            |  (if queue is full → 429)
  |<-- progress (accepted) ----|
  |        ...waiting...       |  (queued until slot available)
  |<-- progress (start) -------|
  |<-- image ------------------|  (if sidecar image output)
  |<-- log (stdout/stderr) ----|
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
  "stdout": "",                         // truncated stdout (on WA/RE only, max 3000 bytes)
  "stderr": "",                         // truncated stderr (on non-zero exit)
  "reason": "",                         // failure reason
  "score": null,                        // nullable float; SPJ score (0.0–1.0)
  "sidecar_outputs": [                  // captured sidecar files
    {"path": "result.txt", "data_b64": "<base64>"}
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
| `Runtime Error` | Non-zero exit or signal |
| `Container Initialization Failed` | Workspace setup or process start failed |

---

## Queue & Rate Limiting

`/execute` uses a bounded concurrency queue:

- **Active slots**: `AONOHAKO_MAX_ACTIVE_RUNS` (default: `max(1, cpu−2)`)
- **Pending queue**: `AONOHAKO_MAX_PENDING_QUEUE` (default: `0` = unlimited)

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
