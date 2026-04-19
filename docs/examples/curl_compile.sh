#!/usr/bin/env bash
set -euo pipefail

# Compile a C++ source file
curl -N -X POST "${GO_URL:-http://localhost:8080}/compile" \
  -H 'Content-Type: application/json' \
  -d '{
    "lang": "CPP17",
    "sources": [{"name":"Main.cpp","data_b64":"I2luY2x1ZGUgPGlvc3RyZWFtPgppbnQgbWFpbigpIHsgc3RkOjpjb3V0IDw8ICJoZWxsbyI7IH0K"}],
    "target": "Main"
  }'
