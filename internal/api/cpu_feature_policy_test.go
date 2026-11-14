package api

import (
	"bytes"
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

func cpuFeatureExecutePayload(t *testing.T, lang, features string, extra map[string]any) []byte {
	t.Helper()
	payload := map[string]any{
		"lang":     lang,
		"binaries": []any{map[string]any{"name": "Main", "data_b64": "eA==", "mode": "exec"}},
		"limits":   map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	if features != "" {
		payload["disallowed_cpu_features"] = json.RawMessage(features)
	}
	for name, value := range extra {
		payload[name] = value
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestExecuteForwardsDisallowedCPUFeatures(t *testing.T) {
	for _, tc := range []struct {
		name, lang, features string
		want                 []cpupolicy.Feature
		measurement          bool
	}{
		{name: "omitted binary", lang: "binary"},
		{name: "omitted other language", lang: "python"},
		{name: "empty other language", lang: "python", features: `[]`},
		{name: "null", lang: "binary", features: `null`},
		{name: "AVX binary", lang: "binary", features: `["avx"]`, want: []cpupolicy.Feature{cpupolicy.AVX}},
		{name: "SIMD native alias", lang: "CPP17", features: `["simd"]`, want: []cpupolicy.Feature{cpupolicy.SIMD}},
		{name: "both", lang: "binary", features: `["simd","avx"]`, want: []cpupolicy.Feature{cpupolicy.SIMD, cpupolicy.AVX}},
		{name: "measurement with omitted policy", lang: "CPP20", measurement: true},
		{name: "measurement with empty policy", lang: "CPP20", features: `[]`, measurement: true},
		{name: "measurement with null policy", lang: "CPP20", features: `null`, measurement: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			s := NewWithServices(configForTest(t), compileRunnerStub{}, executeRunnerStub{run: func(_ context.Context, req *model.RunRequest, _ execute.Hooks) model.RunResponse {
				calls.Add(1)
				if !slices.Equal(req.DisallowedCPUFeatures, tc.want) {
					t.Errorf("runner CPU features = %v, want %v", req.DisallowedCPUFeatures, tc.want)
				}
				if req.WorkMeasurement != tc.measurement {
					t.Errorf("runner work measurement = %v, want %v", req.WorkMeasurement, tc.measurement)
				}
				return model.RunResponse{Status: model.RunStatusAccepted}
			}})
			server := httptest.NewServer(s.Handler())
			defer server.Close()
			response, err := http.Post(server.URL+"/execute", "application/json", bytes.NewReader(cpuFeatureExecutePayload(t, tc.lang, tc.features, map[string]any{"work_measurement": tc.measurement})))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusOK || calls.Load() != 1 || !strings.Contains(string(body), "event: result") {
				t.Fatalf("status = %d, calls = %d, body = %s", response.StatusCode, calls.Load(), body)
			}
		})
	}
}

func TestExecuteRejectsCPUFeaturePolicyBeforeFetchOrAdmission(t *testing.T) {
	var fetches atomic.Int64
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		_, _ = io.WriteString(w, "binary payload")
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)

	for _, tc := range []struct {
		name, lang, features string
		extra                map[string]any
		code                 string
	}{
		{name: "unknown", lang: "binary", features: `["avx2"]`},
		{name: "uppercase", lang: "binary", features: `["AVX"]`},
		{name: "duplicate", lang: "binary", features: `["simd","simd"]`},
		{name: "too many", lang: "binary", features: `["avx","simd","avx"]`},
		{name: "string instead of list", lang: "binary", features: `"avx"`, code: "invalid_json_type"},
		{name: "unsupported language", lang: "python", features: `["avx"]`},
		{name: "network", lang: "binary", features: `["avx"]`, extra: map[string]any{"enable_network": true}},
		{name: "file output", lang: "binary", features: `["avx"]`, extra: map[string]any{"file_outputs": []any{map[string]any{"path": "answer"}}}},
		{name: "sidecar", lang: "binary", features: `["simd"]`, extra: map[string]any{"sidecar_outputs": []any{map[string]any{"path": "image"}}}},
		{name: "communication", lang: "binary", features: `["avx"]`, extra: map[string]any{"communication": map[string]any{}}},
		{name: "work measurement", lang: "CPP20", features: `["avx"]`, extra: map[string]any{"work_measurement": true}},
		{name: "work measurement limits", lang: "CPP20", features: `["simd"]`, extra: map[string]any{"work_measurement": true, "work_measurement_limits": map[string]any{"cpu_time_ms": 2000, "wall_time_ms": 3000, "memory_mb": 128}}},
		{name: "work limits without measurement", lang: "CPP20", features: `["avx"]`, extra: map[string]any{"work_measurement_limits": map[string]any{"cpu_time_ms": 2000, "wall_time_ms": 3000, "memory_mb": 128}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			s := NewWithServices(configForTest(t), compileRunnerStub{}, executeRunnerStub{run: func(_ context.Context, _ *model.RunRequest, _ execute.Hooks) model.RunResponse {
				calls.Add(1)
				return model.RunResponse{Status: model.RunStatusAccepted}
			}})
			extra := map[string]any{"binaries": []any{map[string]any{"name": "Main", "data_url": "http://payload.example/Main", "mode": "exec"}}}
			for name, value := range tc.extra {
				extra[name] = value
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(cpuFeatureExecutePayload(t, tc.lang, tc.features, extra)))
			s.Handler().ServeHTTP(recorder, request)
			var envelope map[string]string
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			code := tc.code
			if code == "" {
				code = "invalid_request"
			}
			if recorder.Code != http.StatusBadRequest || envelope["error"] != code {
				t.Fatalf("status = %d, body = %s; want HTTP 400 %s", recorder.Code, recorder.Body.String(), code)
			}
			if fetches.Load() != 0 || calls.Load() != 0 {
				t.Fatalf("rejected policy fetched or ran: fetches = %d, calls = %d", fetches.Load(), calls.Load())
			}
			if active, pending := s.queue.Snapshot(); active != 0 || pending != 0 || s.streams.Load() != 0 {
				t.Fatalf("admission leaked: active = %d, pending = %d, streams = %d", active, pending, s.streams.Load())
			}
		})
	}
}
