package execute

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

func TestRemotePipelineCapabilityProbe(t *testing.T) {
	contract := runvalidation.PipelineV1Contract()
	body, err := json.Marshal(model.PipelineV1WorkerCapability{Protocol: model.PipelineV1WorkerProtocol, Pipeline: &contract})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		want                    bool
	}{
		{"worker", "application/json; charset=utf-8", string(body), http.StatusOK, true},
		{"unauthorized", "application/json", string(body), http.StatusUnauthorized, false},
		{"wrong media type", "text/html", string(body), http.StatusOK, false},
		{"older protocol", "application/json", strings.Replace(string(body), model.PipelineV1WorkerProtocol, "legacy", 1), http.StatusOK, false},
		{"incomplete contract", "application/json", `{"protocol":"` + model.PipelineV1WorkerProtocol + `","pipeline":{"versions":[1]}}`, http.StatusOK, false},
		{"public capabilities", "application/json", `{"capabilities":["pipeline-v1"]}`, http.StatusOK, false},
		{"trailing JSON", "application/json", string(body) + `{}`, http.StatusOK, false},
		{"oversized", "application/json", string(body) + strings.Repeat(" ", 4096), http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.RequestURI() != "/worker/blue/internal/capabilities/pipeline-v1" ||
					r.Header.Get("Authorization") != "Bearer proof" || r.Header.Get("Accept") != "application/json" {
					t.Errorf("unexpected capability probe: %s %s", r.Method, r.URL.RequestURI())
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer worker.Close()
			runner := newRemoteRunner(config.Config{Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{
				URL: worker.URL + "/worker/blue", Auth: config.RemoteAuthBearer, BearerToken: "proof",
			}}}).(*remoteRunner)
			if got := runner.SupportsPipelineV1(t.Context()); got != tc.want {
				t.Fatalf("SupportsPipelineV1 = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRemotePipelineCapabilityRejectsMissingAuthAndRedirects(t *testing.T) {
	var probes, redirects atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != model.PipelineV1WorkerPath {
			redirects.Add(1)
			return
		}
		probes.Add(1)
		http.Redirect(w, r, "/other-worker", http.StatusTemporaryRedirect)
	}))
	defer worker.Close()
	for _, cfg := range []config.RemoteExecutorConfig{
		{URL: worker.URL, Auth: config.RemoteAuthNone},
		{URL: worker.URL, Auth: config.RemoteAuthBearer},
		{URL: worker.URL, Auth: config.RemoteAuthBearer, BearerToken: "proof"},
	} {
		runner := newRemoteRunner(config.Config{Execution: config.ExecutionConfig{Remote: cfg}}).(*remoteRunner)
		if runner.SupportsPipelineV1(t.Context()) {
			t.Fatal("unverified worker reported support")
		}
	}
	if probes.Load() != 1 || redirects.Load() != 0 {
		t.Fatalf("authenticated probes = %d, redirect targets contacted = %d", probes.Load(), redirects.Load())
	}
}

func TestRemotePipelineCapabilitySharesDeadlineWithCloudRunAuth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := newRemoteRunner(config.Config{Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{
			URL: "https://worker.example", Auth: config.RemoteAuthCloudRunIDToken, Audience: "https://worker.example",
		}}}).(*remoteRunner)
		runner.metadataClient.Transport = executeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			time.Sleep(2 * time.Second)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("id-token"))}, nil
		})
		runner.client.Transport = executeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Header.Get("Authorization") != "Bearer id-token" {
				t.Error("missing Cloud Run ID token")
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		started := time.Now()
		if runner.SupportsPipelineV1(context.Background()) || time.Since(started) != 3*time.Second {
			t.Fatalf("stalled probe with metadata auth took %v", time.Since(started))
		}
	})
}
