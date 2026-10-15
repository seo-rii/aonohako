package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"aonohako/internal/execute"
	"aonohako/internal/model"
)

func TestWorkLifecycleDrainPhases(t *testing.T) {
	s := &Server{}
	preparing, finishPreparing, ok := s.work.begin(context.Background())
	if !ok {
		t.Fatal("preparing admission failed")
	}
	running, finishRunning, ok := s.work.begin(context.Background())
	if !ok || !s.work.start(running) {
		t.Fatal("running admission failed")
	}
	s.BeginDrain()
	s.BeginDrain() // Repeated signals must be harmless.
	if preparing.Err() == nil || running.Err() != nil {
		t.Fatalf("drain contexts: preparing=%v running=%v", preparing.Err(), running.Err())
	}
	if s.work.start(preparing) {
		t.Fatal("draining request started")
	}
	if _, _, admitted := s.work.begin(context.Background()); admitted {
		t.Fatal("request admitted during drain")
	}
	finishPreparing()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s.WaitForWork(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForWork before cleanup = %v", err)
	}
	s.CancelWork()
	if running.Err() == nil {
		t.Fatal("running request not canceled after grace")
	}
	finishRunning()
	if err := s.WaitForWork(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkLifecycleDrainStartRace(t *testing.T) {
	for range 100 {
		s := &Server{}
		ctx, finish, _ := s.work.begin(context.Background())
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); s.work.start(ctx) }()
		go func() { defer wg.Done(); s.BeginDrain() }()
		wg.Wait()
		s.CancelWork()
		finish()
		if err := s.WaitForWork(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDrainRejectsAdmissionAndReadiness(t *testing.T) {
	s := newServerForTest(t)
	s.BeginDrain()
	for _, path := range []string{"/compile", "/execute", "/readyz", "/healthz", "/livez"} {
		body := &readCountingBody{}
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Body = body
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		want := http.StatusServiceUnavailable
		if path == "/livez" {
			want = http.StatusOK
		}
		if w.Code != want || body.reads != 0 {
			t.Fatalf("%s response=%d reads=%d", path, w.Code, body.reads)
		}
	}
	if s.uploads.Load() != 0 || s.streams.Load() != 0 {
		t.Fatal("drain rejection acquired admission")
	}
	if err := s.WaitForWork(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDrainCancelsPreparatoryBodyRead(t *testing.T) {
	s := newServerForTest(t)
	body := newBlockingBody()
	req := httptest.NewRequest(http.MethodPost, "/execute", body)
	done := make(chan struct{})
	go func() { defer close(done); s.Handler().ServeHTTP(httptest.NewRecorder(), req) }()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("body read did not start")
	}
	s.BeginDrain()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("drain did not unblock body read")
	}
	if s.uploads.Load() != 0 {
		t.Fatal("preparation leaked upload admission")
	}
	if err := s.WaitForWork(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDrainInterruptsRealHTTPUpload(t *testing.T) {
	for _, path := range []string{"/compile", "/execute"} {
		for _, chunked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chunked=%v", path, chunked), func(t *testing.T) {
				s := newServerForTest(t)
				s.cfg.MaxActiveUploads = 1
				ts := httptest.NewServer(s.Handler())
				defer ts.Close()
				conn, err := net.Dial("tcp", ts.Listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				length := "Content-Length: 1000"
				partial := `{"lang":"`
				if chunked {
					length = "Transfer-Encoding: chunked"
					partial = fmt.Sprintf("%x\r\n%s\r\n", len(partial), partial)
				}
				if _, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: localhost\r\n%s\r\n\r\n%s", path, length, partial); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(time.Second)
				for s.uploads.Load() != 1 {
					if time.Now().After(deadline) {
						t.Fatal("upload did not acquire preparation admission")
					}
					time.Sleep(time.Millisecond)
				}
				s.BeginDrain()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := s.WaitForWork(ctx); err != nil {
					t.Fatalf("drain did not interrupt blocked socket read: %v", err)
				}
				if s.uploads.Load() != 0 {
					t.Fatal("canceled real upload retained admission")
				}
			})
		}
	}
}

func TestDrainCancelsQueueAndWaitsForRunnerCleanup(t *testing.T) {
	for _, path := range []string{"/compile", "/execute"} {
		t.Run(path, func(t *testing.T) {
			s := newServerForTest(t)
			started, canceled, allowCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
			run := func(ctx context.Context) {
				close(started)
				<-ctx.Done()
				close(canceled)
				<-allowCleanup
			}
			s.compile = compileRunnerStub{run: func(ctx context.Context, _ *model.CompileRequest) model.CompileResponse {
				run(ctx)
				return model.CompileResponse{Status: model.CompileStatusInternal}
			}}
			s.execute = executeRunnerStub{run: func(ctx context.Context, _ *model.RunRequest, _ execute.Hooks) model.RunResponse {
				run(ctx)
				return model.RunResponse{Status: model.RunStatusRE}
			}}
			body := executePayload(t)
			if path == "/compile" {
				var err error
				body, err = json.Marshal(map[string]any{"lang": "PYTHON3", "sources": []map[string]any{{"name": "Main.py", "data_b64": base64.StdEncoding.EncodeToString([]byte("print(1)\n"))}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			activeDone, queuedDone := make(chan struct{}), make(chan struct{})
			handler := s.Handler()
			serve := func(done chan struct{}) {
				defer close(done)
				w := &deadlineResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
				handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
			}
			go serve(activeDone)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("runner did not start")
			}
			go serve(queuedDone)
			deadline := time.Now().Add(time.Second)
			for {
				_, pending := s.queue.Snapshot()
				if pending == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("request did not queue")
				}
				time.Sleep(time.Millisecond)
			}
			s.BeginDrain()
			select {
			case <-queuedDone:
			case <-time.After(time.Second):
				t.Fatal("queued request not canceled")
			}
			select {
			case <-canceled:
				t.Fatal("running job lost completion grace")
			default:
			}
			s.CancelWork()
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("runner not canceled")
			}
			waitDone := make(chan struct{})
			go func() { defer close(waitDone); _ = s.WaitForWork(context.Background()) }()
			select {
			case <-waitDone:
				t.Fatal("shutdown returned before runner cleanup")
			default:
			}
			close(allowCleanup)
			select {
			case <-waitDone:
			case <-time.After(time.Second):
				t.Fatal("cleanup was not observed")
			}
			<-activeDone
			active, pending := s.queue.Snapshot()
			if active != 0 || pending != 0 || s.uploads.Load() != 0 || s.streams.Load() != 0 {
				t.Fatalf("leaked admission: active=%d pending=%d uploads=%d streams=%d", active, pending, s.uploads.Load(), s.streams.Load())
			}
		})
	}
}
