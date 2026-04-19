#!/usr/bin/env bash
set -euo pipefail

curl -N -X POST "${GO_URL:-http://localhost:8080}/execute" \
  -H 'Content-Type: application/json' \
  -d '{
    "lang": "binary",
    "binaries": [{"name":"run.sh","mode":"exec","data_b64":"IyEvYmluL3NoCmNhdAo="}],
    "stdin": "hello\n",
    "expected_stdout": "hello\n",
    "limits": {"time_ms": 2000, "memory_mb": 64}
  }'
