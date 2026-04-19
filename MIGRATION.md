# Migration to aonohako

## From legacy services

- `saet/run_set` build responsibilities are moved to `POST /compile`.
- `saet/run_go` execution responsibilities are moved to `POST /execute`.
- Both endpoints now stream SSE events and finish with a single `result` event.

## Integration checklist

1. Replace JSON build call (`run_set /build`) with SSE compile call (`aonohako /compile`).
2. Replace JSON execute call (`run_go /run`) with SSE execute call (`aonohako /execute`).
3. Handle `429 {"error":"queue_full"}` using caller-side retry/backoff.
4. Consume `image` and `log` events while waiting for final `result`.
