package api

import (
	"aonohako/internal/config"
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExecuteQueueOverflowReturns429(t *testing.T) {
	s := New(configForTest(t))
	h := s.Handler()
	ts := httptest.NewServer(h)
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("import time\ntime.sleep(2)\n"))
	payload := map[string]any{
		"lang":     "python",
		"binaries": []map[string]any{{"name": "main.py", "data_b64": script}},
		"limits":   map[string]any{"time_ms": 5000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)

	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("req1 failed: %v", err)
	}
	defer resp1.Body.Close()

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("req2 failed: %v", err)
	}
	defer resp2.Body.Close()

	time.Sleep(100 * time.Millisecond)

	req3, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("req3 failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp3.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	s := New(configForTest(t))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("unexpected healthz response: %q", string(body))
	}
}

func TestExecuteSSESequence(t *testing.T) {
	s := New(configForTest(t))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n"))
	payload := map[string]any{
		"lang":            "binary",
		"binaries":        []map[string]any{{"name": "run.sh", "data_b64": script, "mode": "exec"}},
		"expected_stdout": "",
		"limits":          map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	events := readSSEEvents(resp.Body, t)
	if len(events) < 3 {
		t.Fatalf("expected at least 3 events, got %d", len(events))
	}
	if events[0].Name != "progress" {
		t.Fatalf("first event should be progress, got %s", events[0].Name)
	}
	if events[0].JSON["stage"] != "accepted" {
		t.Fatalf("first progress stage should be accepted: %#v", events[0].JSON)
	}
	if events[1].Name != "progress" || events[1].JSON["stage"] != "start" {
		t.Fatalf("second event should be start progress: %#v", events[1])
	}
	last := events[len(events)-1]
	if last.Name != "result" {
		t.Fatalf("last event should be result, got %s", last.Name)
	}
	if last.JSON["status"] != "Accepted" {
		t.Fatalf("unexpected run status in result: %#v", last.JSON)
	}
	if _, ok := last.JSON["wall_time_ms"]; !ok {
		t.Fatalf("result missing wall_time_ms: %#v", last.JSON)
	}
	if _, ok := last.JSON["cpu_time_ms"]; !ok {
		t.Fatalf("result missing cpu_time_ms: %#v", last.JSON)
	}
	if last.JSON["time_ms"] != last.JSON["wall_time_ms"] {
		t.Fatalf("time_ms should mirror wall_time_ms for compatibility: %#v", last.JSON)
	}
}

type sseEvent struct {
	Name string
	JSON map[string]any
}

func readSSEEvents(r io.Reader, t *testing.T) []sseEvent {
	t.Helper()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 16*1024), 2*1024*1024)
	events := make([]sseEvent, 0, 8)
	name := ""
	data := ""
	dispatch := func() {
		if name == "" || data == "" {
			name = ""
			data = ""
			return
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			t.Fatalf("invalid json payload for %s: %v", name, err)
		}
		events = append(events, sseEvent{Name: name, JSON: parsed})
		name = ""
		data = ""
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			dispatch()
			if len(events) > 0 && events[len(events)-1].Name == "result" {
				return events
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("sse scan failed: %v", err)
	}
	return events
}

func configForTest(t *testing.T) config.Config {
	t.Helper()
	return config.Config{Port: "0", MaxActiveRuns: 1, MaxPendingQueue: 1, HeartbeatInterval: 100 * time.Millisecond}
}

// --------------- #3: /compile shares queue with /execute ---------------

func TestCompileQueueOverflowReturns429(t *testing.T) {
	s := New(configForTest(t))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	script := base64.StdEncoding.EncodeToString([]byte("import time\ntime.sleep(2)\n"))
	compilePayload := map[string]any{
		"lang":    "CPP17",
		"sources": []map[string]any{{"name": "Main.cpp", "data_b64": base64.StdEncoding.EncodeToString([]byte("int main(){}"))}},
	}
	execPayload := map[string]any{
		"lang":     "python",
		"binaries": []map[string]any{{"name": "main.py", "data_b64": script}},
		"limits":   map[string]any{"time_ms": 5000, "memory_mb": 64},
	}

	// Fill the queue with execute request
	execBody, _ := json.Marshal(execPayload)
	req1, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(execBody))
	req1.Header.Set("Content-Type", "application/json")
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("execute req failed: %v", err)
	}
	defer resp1.Body.Close()

	// Fill the pending slot with another execute
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", bytes.NewReader(execBody))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("execute req2 failed: %v", err)
	}
	defer resp2.Body.Close()

	time.Sleep(100 * time.Millisecond)

	// Now compile should also get 429 since it shares the same queue
	compileBody, _ := json.Marshal(compilePayload)
	req3, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(compileBody))
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("compile req failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected compile to get 429 (shared queue), got %d", resp3.StatusCode)
	}
}

func TestCompileSSEHasProgressEvents(t *testing.T) {
	s := New(configForTest(t))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":    "CPP17",
		"sources": []map[string]any{{"name": "Main.cpp", "data_b64": base64.StdEncoding.EncodeToString([]byte("int main(){}"))}},
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	events := readSSEEvents(resp.Body, t)
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (progress+result), got %d", len(events))
	}
	if events[0].Name != "progress" {
		t.Fatalf("first event should be progress, got %s", events[0].Name)
	}
	if events[0].JSON["stage"] != "accepted" {
		t.Fatalf("first progress stage should be accepted: %#v", events[0].JSON)
	}
}

func TestCompileMethodNotAllowed(t *testing.T) {
	s := New(configForTest(t))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/compile")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /compile, got %d", resp.StatusCode)
	}
}

func TestExecuteMethodNotAllowed(t *testing.T) {
	s := New(configForTest(t))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/execute")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET /execute, got %d", resp.StatusCode)
	}
}

func TestCompileInvalidJSON(t *testing.T) {
	s := New(configForTest(t))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/compile", strings.NewReader("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", resp.StatusCode)
	}
}

func TestExecuteInvalidJSON(t *testing.T) {
	s := New(configForTest(t))
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/execute", strings.NewReader("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid JSON, got %d", resp.StatusCode)
	}
}

type noFlushResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *noFlushResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *noFlushResponseWriter) WriteHeader(code int) {
	w.status = code
}

func (w *noFlushResponseWriter) Write(p []byte) (int, error) {
	return w.body.Write(p)
}

func TestCompileSSEInitFailureReleasesPermit(t *testing.T) {
	s := New(configForTest(t))
	payload := map[string]any{
		"lang":    "UHMLANG",
		"sources": []map[string]any{{"name": "Main.uhm", "data_b64": base64.StdEncoding.EncodeToString([]byte("text"))}},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/compile", bytes.NewReader(body))

	w := &noFlushResponseWriter{}
	s.compileHandler(w, req)

	active, pending := s.queue.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("queue leaked after compile SSE init failure: active=%d pending=%d", active, pending)
	}
}

func TestExecuteSSEInitFailureReleasesPermit(t *testing.T) {
	s := New(configForTest(t))
	payload := map[string]any{
		"lang":     "binary",
		"binaries": []map[string]any{{"name": "run.sh", "data_b64": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho ok\n")), "mode": "exec"}},
		"limits":   map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body))

	w := &noFlushResponseWriter{}
	s.executeHandler(w, req)

	active, pending := s.queue.Snapshot()
	if active != 0 || pending != 0 {
		t.Fatalf("queue leaked after execute SSE init failure: active=%d pending=%d", active, pending)
	}
}
