# Migration to aonohako

## From legacy services

- `saet/run_set` build responsibilities are moved to `POST /compile`.
- `saet/run_go` execution responsibilities are moved to `POST /execute`.
- Both endpoints now stream SSE events and finish with a single `result` event.

## Integration checklist

1. Replace JSON build call (`run_set /build`) with SSE compile call (`aonohako /compile`).
2. Replace JSON execute call (`run_go /run`) with SSE execute call (`aonohako /execute`).
3. Treat `/compile` and `/execute` as streamed endpoints that always finish
   with exactly one `result` event. `log`, `image`, `error`, `heartbeat`, and
   `progress` events can arrive before the final result.
4. Handle all 429 admission errors with caller-side retry/backoff:
   `stream_limit_exceeded`, `principal_stream_limit_exceeded`,
   `principal_rate_limited`, and `queue_full`.
5. Forward `problem_id` when the control plane owns runtime tuning policy.
   Only forward `runtime_profile` directly on trusted boundaries where
   `AONOHAKO_ALLOW_REQUEST_RUNTIME_PROFILE=true`.
6. Use the same `/execute` endpoint for two-step problems. Send `programs` and
   exactly two `steps`; do not mix legacy top-level `lang`, `binaries`,
   `stdin`, `limits`, `entry_point`, or `enable_network` fields with the step
   pipeline shape.
7. Expect `ONLINE_JUDGE=1` in compile and execute environments. Languages with
   compiler-supported defines or build tags also receive the matching
   compile-time `ONLINE_JUDGE` flag.
8. For remote runners in non-dev deployments, ensure the downstream runner
   returns `X-Aonohako-Protocol-Version` and that control-plane and runner
   runtime profile config stay synchronized.
