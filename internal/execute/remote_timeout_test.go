package execute

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"testing"
	"testing/synctest"
	"time"

	"aonohako/internal/model"
)

func TestRemoteRunnerPreservesAllowedStdinDownloadTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &remoteRunner{
			client: &http.Client{Transport: executeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(req.Context())
				if trace != nil && trace.WroteRequest != nil {
					trace.WroteRequest(httptrace.WroteRequestInfo{})
				}
				reader, writer := io.Pipe()
				go func() {
					defer writer.Close()
					for i := 0; i < 8; i++ {
						select {
						case <-req.Context().Done():
							_ = writer.CloseWithError(req.Context().Err())
							return
						case <-time.After(5 * time.Second):
							if _, err := fmt.Fprint(writer, ": heartbeat\n\n"); err != nil {
								return
							}
						}
					}
					_, _ = fmt.Fprint(writer, "event: result\ndata: {\"status\":\"Accepted\"}\n\n")
				}()
				return &http.Response{
					StatusCode: http.StatusOK, Status: "200 OK",
					Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader,
				}, nil
			})},
			executeURL: "http://remote.example/execute",
		}
		response := runner.Run(context.Background(), &model.RunRequest{
			StdinURL: "https://input.example/test.in", Limits: model.Limits{TimeMs: 1000},
		}, Hooks{})
		if response.Status != model.RunStatusAccepted {
			t.Fatalf("allowed 40-second input download lost its execution budget: %s: %s", response.Status, response.Reason)
		}
	})
}
