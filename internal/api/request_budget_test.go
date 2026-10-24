package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/execute"
	"aonohako/internal/model"
)

func retainedBytesForTest(s *Server) int64 {
	s.requestBytes.mu.Lock()
	defer s.requestBytes.mu.Unlock()
	return s.requestBytes.used
}

func TestRequestByteBudgetPersistsAcrossQueueAndCancellation(t *testing.T) {
	cfg := configForTest(t)
	cfg.MaxRetainedRequestBytes = 8000
	cfg.MaxPendingQueue = 16
	cfg.MaxActiveUploads = 1
	started := make(chan struct{})
	s := NewWithServices(cfg, compileRunnerStub{}, executeRunnerStub{run: func(ctx context.Context, _ *model.RunRequest, _ execute.Hooks) model.RunResponse {
		close(started)
		<-ctx.Done()
		return model.RunResponse{Status: model.RunStatusRE}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	defer func() { s.BeginDrain(); s.CancelWork() }()
	var req model.RunRequest
	if err := json.Unmarshal(executePayload(t), &req); err != nil {
		t.Fatal(err)
	}
	req.Stdin = strings.Repeat("x", 3000)
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		response, err := http.Post(ts.URL+"/execute", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("request %d status=%d", i, response.StatusCode)
		}
		if i == 0 {
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("first execution did not start")
			}
		}
	}
	if active, pending := s.queue.Snapshot(); active != 1 || pending != 1 {
		t.Fatalf("queue active=%d pending=%d", active, pending)
	}
	if got := retainedBytesForTest(s); got < 6000 || got > cfg.MaxRetainedRequestBytes {
		t.Fatalf("queued requests retain %d bytes", got)
	}
	if s.uploads.Load() != 0 {
		t.Fatal("upload slot should be released while queued")
	}
	countingBody := &readCountingBody{}
	next := httptest.NewRequest(http.MethodPost, "/execute", nil)
	next.ContentLength = int64(len(body))
	next.Body = countingBody
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, next)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "request_byte_budget_exceeded") || countingBody.reads != 0 {
		t.Fatalf("budget rejection: status=%d body=%s reads=%d", w.Code, w.Body.String(), countingBody.reads)
	}
	s.BeginDrain()
	s.CancelWork()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.WaitForWork(ctx); err != nil {
		t.Fatal(err)
	}
	if got := retainedBytesForTest(s); got != 0 {
		t.Fatalf("canceled work retained %d bytes", got)
	}
}

func TestRequestByteBudgetReleasedOnHandlerFailures(t *testing.T) {
	for _, path := range []string{"/compile", "/execute"} {
		for _, body := range []string{`{`, `{}`, `{"unknown":true}`} {
			t.Run(path+body, func(t *testing.T) {
				s := newServerForTest(t)
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				if retainedBytesForTest(s) != 0 || s.uploads.Load() != 0 {
					t.Fatal("failed request leaked admission")
				}
			})
		}
	}
}

func TestRequestByteBudgetRejectsUnknownLengthBeforeRead(t *testing.T) {
	cfg := configForTest(t)
	cfg.MaxRetainedRequestBytes = MaxJSONBodyBytes - 1
	s := NewWithServices(cfg, compileRunnerStub{}, executeRunnerStub{})
	body := &readCountingBody{}
	req := httptest.NewRequest(http.MethodPost, "/execute", body)
	req.ContentLength = -1
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests || body.reads != 0 || retainedBytesForTest(s) != 0 {
		t.Fatalf("status=%d reads=%d", w.Code, body.reads)
	}
}

func TestPayloadByteReservationPrecedesFetchAndShrinks(t *testing.T) {
	var fetches atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, "abc")
	}))
	defer ts.Close()
	setPayloadURLHTTPClientForTest(t, ts.URL)
	budget := &requestByteBudget{limit: 12}
	reservation := &requestByteReservation{budget: budget}
	if err := reservation.resize(5); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), requestByteContextKey{}, reservation)
	inline, url := "", "http://payload.example/data"
	err := resolveBase64URL(ctx, "binary", &inline, &url, 6, nil)
	if !errors.Is(err, errRequestByteBudget) || fetches.Load() != 0 || reservation.held != 5 {
		t.Fatalf("pre-fetch reservation: err=%v fetches=%d held=%d", err, fetches.Load(), reservation.held)
	}
	if err := reservation.resize(4); err != nil {
		t.Fatal(err)
	}
	if err := resolveBase64URL(ctx, "binary", &inline, &url, 6, nil); err != nil {
		t.Fatal(err)
	}
	if inline != base64.StdEncoding.EncodeToString([]byte("abc")) || url != "" || reservation.held != 8 || budget.used != 8 {
		t.Fatalf("successful reservation: inline=%q url=%q held=%d used=%d", inline, url, reservation.held, budget.used)
	}
	inline, url = "", "http://payload.example/fail"
	if err := resolveTextURL(ctx, "answer", &inline, &url, 4); err == nil {
		t.Fatal("failed fetch accepted")
	}
	if reservation.held != 8 || budget.used != 8 {
		t.Fatal("failed download leaked reservation")
	}
	if err := reservation.resize(0); err != nil || budget.used != 0 {
		t.Fatal("release failed")
	}
}

func TestPayloadByteBudgetReturns429BeforeSSE(t *testing.T) {
	for _, path := range []string{"/compile", "/execute"} {
		t.Run(path, func(t *testing.T) {
			cfg := configForTest(t)
			cfg.MaxRetainedRequestBytes = 1 << 20
			s := NewWithServices(cfg, compileRunnerStub{}, executeRunnerStub{})
			var body string
			if path == "/compile" {
				body = `{"lang":"TEXT","sources":[{"name":"Main.txt","data_url":"https://payload.example/source"}]}`
			} else {
				body = `{"lang":"binary","binaries":[{"name":"Main","data_url":"https://payload.example/binary","mode":"exec"}],"limits":{"time_ms":1000,"memory_mb":64}}`
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
			if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "request_byte_budget_exceeded") {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if retainedBytesForTest(s) != 0 || len(s.payloadURLFetchSlots) != 0 {
				t.Fatal("URL rejection leaked capacity")
			}
		})
	}
}

func TestRequestStringBytesCoversNestedPipeline(t *testing.T) {
	req := model.RunRequest{Pipeline: &model.PipelineV1{
		Resources: map[string]model.PipelineResource{"key": {DataB64: "YWJj"}},
		Programs:  []model.RunProgram{{ID: "p", Binaries: []model.Binary{{Name: "Main", DataB64: "eA=="}}}},
	}}
	if got := requestStringBytes(reflect.ValueOf(&req)); got != 16 {
		t.Fatalf("retained strings=%d, want16", got)
	}
}

func TestRequestStringBytesIncludesShrunkenArrayBacking(t *testing.T) {
	// encoding/json retains the second element when a duplicate field shrinks
	// the slice. The queued request still owns its backing array and payload.
	req := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(`{"steps":[{"stdin":"one"},{"stdin":"retained"}],"steps":[{}]}`))
	var decoded model.RunRequest
	if err := decodeJSONBody(httptest.NewRecorder(), req, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Steps) != 1 || cap(decoded.Steps) < 2 {
		t.Fatalf("unexpected duplicate array shape: len=%d cap=%d", len(decoded.Steps), cap(decoded.Steps))
	}
	if got := requestStringBytes(reflect.ValueOf(&decoded)); got != int64(len("oneretained")) {
		t.Fatalf("retained strings=%d, want11", got)
	}
}

func TestRequestByteReservationsConcurrent(t *testing.T) {
	budget := &requestByteBudget{limit: 64}
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &requestByteReservation{budget: budget}
			if err := r.resize(16); err != nil {
				return
			}
			budget.mu.Lock()
			if budget.used > budget.limit {
				t.Errorf("oversubscribed: %d", budget.used)
			}
			budget.mu.Unlock()
			_ = r.resize(0)
		}()
	}
	wg.Wait()
	if budget.used != 0 {
		t.Fatalf("retained bytes=%d", budget.used)
	}
}

func TestRestoredHMACBodyDropsBufferWhenDecoded(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(`{"stdin":"hello"}`))
	if _, err := hashAndRestoreRequestBody(httptest.NewRecorder(), req); err != nil {
		t.Fatal(err)
	}
	outerBody := req.Body.(*restoredRequestBody)
	innerRequest := req.WithContext(context.Background())
	var decoded model.RunRequest
	if err := decodeJSONBody(httptest.NewRecorder(), innerRequest, &decoded); err != nil {
		t.Fatal(err)
	}
	if outerBody.Size() != 0 || decoded.Stdin != "hello" {
		t.Fatal("outer request copy retained the restored HMAC bytes")
	}
}
