package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/payloadurl"
	"aonohako/internal/runvalidation"
)

func setPayloadURLHTTPClientForTest(t *testing.T, targetURL string) {
	t.Helper()
	parsed, err := url.Parse(targetURL)
	if err != nil {
		t.Fatalf("parse test payload URL: %v", err)
	}
	dialer := &net.Dialer{}
	testClient := payloadurl.NewHTTPClientWithOptions(payloadurl.ClientOptions{
		Timeout: time.Second,
		Resolver: payloadurl.LookupIPAddrFunc(func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}),
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, parsed.Host)
		},
	})
	previous := payloadURLHTTPClient
	payloadURLHTTPClient = testClient
	t.Cleanup(func() {
		testClient.CloseIdleConnections()
		payloadURLHTTPClient = previous
	})
}

func TestDownloadPayloadURLRejectsLoopbackBeforeRequest(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "secret")
	}))
	defer server.Close()

	if _, err := downloadPayloadURL(context.Background(), server.URL, 16); err == nil || !strings.Contains(err.Error(), "no public addresses") {
		t.Fatalf("downloadPayloadURL error = %v, want loopback rejection", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("private server received %d requests", got)
	}
}

func TestResolvePayloadURLsConsumeDataURLFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "payload")
	}))
	defer server.Close()
	setPayloadURLHTTPClientForTest(t, server.URL)

	compileReq := &model.CompileRequest{Sources: []model.Source{{Name: "Main.go", DataURL: "http://payload.example/source"}}}
	if err := resolveCompilePayloadURLs(context.Background(), compileReq, 0); err != nil {
		t.Fatal(err)
	}
	if compileReq.Sources[0].DataURL != "" || compileReq.Sources[0].DataB64 != base64.StdEncoding.EncodeToString([]byte("payload")) {
		t.Fatalf("compile source was not consumed: %+v", compileReq.Sources[0])
	}

	runReq := &model.RunRequest{
		ExpectedStdoutURL: "http://payload.example/expected",
		Binaries:          []model.Binary{{Name: "legacy", DataURL: "http://payload.example/legacy"}},
		Programs: []model.RunProgram{{
			ID:       "program",
			Binaries: []model.Binary{{Name: "program", DataURL: "http://payload.example/program"}},
		}},
		SPJ:        &model.SPJSpec{Binary: &model.Binary{Name: "spj", DataURL: "http://payload.example/spj"}},
		Interactor: &model.InteractorSpec{Binaries: []model.Binary{{Name: "interactor", DataURL: "http://payload.example/interactor"}}},
	}
	if err := resolveRunPayloadURLs(context.Background(), runReq); err != nil {
		t.Fatal(err)
	}
	if runReq.ExpectedStdoutURL != "" || runReq.ExpectedStdout != "payload" {
		t.Fatalf("expected stdout URL was not consumed: %+v", runReq)
	}
	for label, binary := range map[string]model.Binary{
		"legacy":     runReq.Binaries[0],
		"program":    runReq.Programs[0].Binaries[0],
		"spj":        *runReq.SPJ.Binary,
		"interactor": runReq.Interactor.Binaries[0],
	} {
		if binary.DataURL != "" || binary.DataB64 == "" {
			t.Fatalf("%s URL was not consumed: %+v", label, binary)
		}
	}
}

func TestResolveRunPayloadURLsValidatesAllConflictsBeforeFetch(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "payload")
	}))
	defer server.Close()
	setPayloadURLHTTPClientForTest(t, server.URL)

	req := &model.RunRequest{
		ExpectedStdoutURL: "http://payload.example/expected",
		Binaries: []model.Binary{{
			Name:    "runner",
			DataB64: "cGF5bG9hZA==",
			DataURL: "http://payload.example/runner",
		}},
	}
	if err := resolveRunPayloadURLs(context.Background(), req); err == nil || !strings.Contains(err.Error(), "cannot combine") {
		t.Fatalf("resolveRunPayloadURLs error = %v, want conflict", err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("invalid request performed %d fetches", got)
	}
}

func TestResolveBase64URLStopsAtAggregateBudget(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "ab")
	}))
	defer server.Close()
	setPayloadURLHTTPClientForTest(t, server.URL)

	budget := &payloadByteBudget{remaining: 3, limit: 3, message: "payload total size exceeded"}
	firstData, firstURL := "", "http://payload.example/first"
	if err := resolveBase64URL(context.Background(), "first", &firstData, &firstURL, 16, budget); err != nil {
		t.Fatal(err)
	}
	secondData, secondURL := "", "http://payload.example/second"
	err := resolveBase64URL(context.Background(), "second", &secondData, &secondURL, 16, budget)
	if err == nil || !strings.Contains(err.Error(), "payload total size exceeded") {
		t.Fatalf("second resolution error = %v, want aggregate limit", err)
	}
	if firstURL != "" || firstData == "" || secondURL == "" || secondData != "" {
		t.Fatalf("unexpected retained fields: first=(%q,%q) second=(%q,%q)", firstData, firstURL, secondData, secondURL)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestCompileRejectsTooManyURLSourcesWithoutFetch(t *testing.T) {
	var requests atomic.Int64
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "package main")
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)

	s := NewWithServices(configForTest(t), compileRunnerStub{run: func(context.Context, *model.CompileRequest) model.CompileResponse {
		t.Fatal("compile runner called")
		return model.CompileResponse{}
	}}, execute.New())
	server := httptest.NewServer(s.Handler())
	defer server.Close()

	sources := make([]map[string]any, maxCompileSourceFiles+1)
	for i := range sources {
		sources[i] = map[string]any{"name": fmt.Sprintf("%03d.go", i), "data_url": "http://payload.example/source"}
	}
	body, err := json.Marshal(map[string]any{"lang": "go", "sources": sources})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(server.URL+"/compile", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("over-count request performed %d fetches", got)
	}
}

func TestExecuteRejectsTooManyURLBinariesWithoutFetch(t *testing.T) {
	var requests atomic.Int64
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "runner")
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)

	s := NewWithServices(configForTest(t), compileRunnerStub{}, executeRunnerStub{run: func(context.Context, *model.RunRequest, execute.Hooks) model.RunResponse {
		t.Fatal("execute runner called")
		return model.RunResponse{}
	}})
	server := httptest.NewServer(s.Handler())
	defer server.Close()

	binaries := make([]map[string]any, runvalidation.MaxBinaryFiles+1)
	for i := range binaries {
		binaries[i] = map[string]any{"name": fmt.Sprintf("%03d", i), "data_url": "http://payload.example/runner"}
	}
	body, err := json.Marshal(map[string]any{
		"lang":     "binary",
		"binaries": binaries,
		"limits":   map[string]any{"time_ms": 1000, "memory_mb": 64},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(server.URL+"/execute", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("over-count request performed %d fetches", got)
	}
}

func TestExecuteRejectsDisallowedNetworkBeforePayloadFetch(t *testing.T) {
	var requests atomic.Int64
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "runner")
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)

	s := NewWithServices(configForTest(t), compileRunnerStub{}, executeRunnerStub{run: func(context.Context, *model.RunRequest, execute.Hooks) model.RunResponse {
		t.Fatal("execute runner called")
		return model.RunResponse{}
	}})
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	body, _ := json.Marshal(map[string]any{
		"lang":           "binary",
		"binaries":       []map[string]any{{"name": "runner", "data_url": "http://payload.example/runner"}},
		"enable_network": true,
		"limits":         map[string]any{"time_ms": 1000, "memory_mb": 64},
	})
	resp, err := http.Post(server.URL+"/execute", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("network-rejected request performed %d fetches", got)
	}
}

func TestPayloadURLFetchAdmissionRejectsBeforeFetch(t *testing.T) {
	var requests atomic.Int64
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, "package main")
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)

	s := NewWithServices(configForTest(t), compileRunnerStub{}, execute.New())
	s.payloadURLFetchSlots <- struct{}{}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	body, _ := json.Marshal(map[string]any{
		"lang":    "go",
		"sources": []map[string]any{{"name": "Main.go", "data_url": "http://payload.example/source"}},
	})
	resp, err := http.Post(server.URL+"/compile", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("admission-rejected request performed %d fetches", got)
	}
}

func TestPayloadURLFetchUsesRequestWideDeadline(t *testing.T) {
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer assetServer.Close()
	setPayloadURLHTTPClientForTest(t, assetServer.URL)

	s := NewWithServices(configForTest(t), compileRunnerStub{}, execute.New())
	s.payloadURLTimeout = 20 * time.Millisecond
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	body, _ := json.Marshal(map[string]any{
		"lang":    "go",
		"sources": []map[string]any{{"name": "Main.go", "data_url": "http://payload.example/source"}},
	})
	resp, err := http.Post(server.URL+"/compile", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 504", resp.StatusCode, string(raw))
	}
}
