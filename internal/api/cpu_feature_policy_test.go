package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/execute"
	"aonohako/internal/model"
)

func cpuFeatureCompilePayload(t *testing.T, lang, features, sourceURL string) []byte {
	t.Helper()
	source := map[string]any{"name": "Main.cpp", "data_b64": "aW50IG1haW4oKSB7IHJldHVybiAwOyB9Cg=="}
	if sourceURL != "" {
		delete(source, "data_b64")
		source["data_url"] = sourceURL
	}
	payload := map[string]any{"lang": lang, "sources": []any{source}}
	if features != "" {
		payload["disallowed_cpu_features"] = json.RawMessage(features)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal CPU policy request: %v", err)
	}
	return body
}

func TestCompileForwardsDisallowedCPUFeatures(t *testing.T) {
	for _, tc := range []struct {
		name, lang, features string
		want                 []cpupolicy.Feature
	}{
		{name: "omitted C++", lang: "CPP17"},
		{name: "omitted other language", lang: "PYTHON3"},
		{name: "empty C", lang: "C11", features: `[]`},
		{name: "empty other language", lang: "PYTHON3", features: `[]`},
		{name: "null", lang: "CPP17", features: `null`},
		{name: "AVX C alias", lang: "c", features: `["avx"]`, want: []cpupolicy.Feature{cpupolicy.AVX}},
		{name: "SIMD C++ version", lang: "CPP23", features: `["simd"]`, want: []cpupolicy.Feature{cpupolicy.SIMD}},
		{name: "combined C++ alias", lang: "c++", features: `["avx","simd"]`, want: []cpupolicy.Feature{cpupolicy.AVX, cpupolicy.SIMD}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			s := NewWithServices(configForTest(t), compileRunnerStub{run: func(_ context.Context, req *model.CompileRequest) model.CompileResponse {
				calls.Add(1)
				if !slices.Equal(req.DisallowedCPUFeatures, tc.want) {
					t.Errorf("runner CPU features = %v, want %v", req.DisallowedCPUFeatures, tc.want)
				}
				return model.CompileResponse{Status: model.CompileStatusOK}
			}}, execute.New())
			server := httptest.NewServer(s.Handler())
			defer server.Close()

			status, body := postGoModulePolicyRequest(t, server.URL, cpuFeatureCompilePayload(t, tc.lang, tc.features, ""))
			if status != http.StatusOK || calls.Load() != 1 || !strings.Contains(body, "event: result") {
				t.Fatalf("status = %d, runner calls = %d, body = %s", status, calls.Load(), body)
			}
		})
	}
}

func TestCompileRejectsDisallowedCPUFeaturesBeforeFetchOrAdmission(t *testing.T) {
	var fetches atomic.Int64
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		_, _ = io.WriteString(w, "int main() { return 0; }\n")
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)

	for _, tc := range []struct {
		name, lang, features, code string
	}{
		{name: "unknown", lang: "CPP17", features: `["avx2"]`, code: "invalid_disallowed_cpu_features"},
		{name: "uppercase", lang: "CPP17", features: `["AVX"]`, code: "invalid_disallowed_cpu_features"},
		{name: "whitespace", lang: "CPP17", features: `[" simd"]`, code: "invalid_disallowed_cpu_features"},
		{name: "empty name", lang: "CPP17", features: `[""]`, code: "invalid_disallowed_cpu_features"},
		{name: "null name", lang: "CPP17", features: `[null]`, code: "invalid_disallowed_cpu_features"},
		{name: "duplicate", lang: "CPP17", features: `["simd","simd"]`, code: "invalid_disallowed_cpu_features"},
		{name: "too many", lang: "CPP17", features: `["simd","avx","simd"]`, code: "invalid_request"},
		{name: "unsupported language", lang: "PYTHON3", features: `["simd"]`, code: "invalid_disallowed_cpu_features"},
		{name: "assembly language", lang: "ASM", features: `["avx"]`, code: "invalid_disallowed_cpu_features"},
		{name: "string instead of array", lang: "CPP17", features: `"avx"`, code: "invalid_json_type"},
		{name: "object instead of array", lang: "CPP17", features: `{"avx":true}`, code: "invalid_json_type"},
		{name: "non-string element", lang: "CPP17", features: `[true]`, code: "invalid_json_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			s := NewWithServices(configForTest(t), compileRunnerStub{run: func(_ context.Context, _ *model.CompileRequest) model.CompileResponse {
				calls.Add(1)
				return model.CompileResponse{Status: model.CompileStatusOK}
			}}, execute.New())
			server := httptest.NewServer(s.Handler())
			defer server.Close()

			status, body := postGoModulePolicyRequest(t, server.URL, cpuFeatureCompilePayload(t, tc.lang, tc.features, "http://payload.example/Main.cpp"))
			var envelope map[string]string
			if err := json.Unmarshal([]byte(body), &envelope); err != nil {
				t.Fatalf("decode error response: %v; body = %s", err, body)
			}
			if status != http.StatusBadRequest || envelope["error"] != tc.code {
				t.Fatalf("status = %d, body = %s; want HTTP 400 %s", status, body, tc.code)
			}
			if calls.Load() != 0 || fetches.Load() != 0 {
				t.Fatalf("invalid policy reached runner or source fetch: runner = %d, fetches = %d", calls.Load(), fetches.Load())
			}
			if active, pending := s.queue.Snapshot(); active != 0 || pending != 0 || s.streams.Load() != 0 {
				t.Fatalf("admission leaked: active = %d, pending = %d, streams = %d", active, pending, s.streams.Load())
			}
		})
	}
}

func TestCompileDisallowedCPUFeaturesReplacesEarlierArray(t *testing.T) {
	for _, tc := range []struct {
		name, last string
		want       []cpupolicy.Feature
	}{
		{name: "shrink", last: `["avx"]`, want: []cpupolicy.Feature{cpupolicy.AVX}},
		{name: "empty", last: `[]`},
		{name: "null", last: `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			s := NewWithServices(configForTest(t), compileRunnerStub{run: func(_ context.Context, req *model.CompileRequest) model.CompileResponse {
				calls.Add(1)
				if !slices.Equal(req.DisallowedCPUFeatures, tc.want) {
					t.Errorf("runner retained earlier CPU features: got %v, want %v", req.DisallowedCPUFeatures, tc.want)
				}
				return model.CompileResponse{Status: model.CompileStatusOK}
			}}, execute.New())
			server := httptest.NewServer(s.Handler())
			defer server.Close()
			base := cpuFeatureCompilePayload(t, "CPP17", "", "")
			body := string(base[:len(base)-1]) + `,"disallowed_cpu_features":["simd","avx"],"disallowed_cpu_features":` + tc.last + `}`
			status, response := postGoModulePolicyRequest(t, server.URL, []byte(body))
			if status != http.StatusOK || calls.Load() != 1 {
				t.Fatalf("status = %d, calls = %d, body = %s", status, calls.Load(), response)
			}
		})
	}
}
