package execute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"aonohako/internal/model"
)

func TestRemoteRunnerPreservesPeerCPUAccounting(t *testing.T) {
	for _, mode := range []string{"interactive", "communication-v1"} {
		for _, measured := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/measurement=%t", mode, measured), func(t *testing.T) {
				req := peerMeasurementRequest(mode)
				req.WorkMeasurement = measured
				req.Limits = model.Limits{TimeMs: 1000, MemoryMB: 128}
				zero := 0
				// Each native role stays within its own 1000ms CPU allowance;
				// the existing coordinator records their sum at the top level.
				process := execResult{Status: "OK", ExitCode: &zero, CPUTimeMs: 700, MemoryKB: 32768}
				var local model.RunResponse
				if mode == "interactive" {
					local = interactiveResponse(req, interactorRunRequest(req), process, process, 900, false, false)
				} else {
					participantReq := communicationProgramRequest(req, req.Programs[1], req.Limits)
					managerReq := communicationProgramRequest(req, req.Programs[0], communicationManagerLimits(req.Limits))
					score := 1.0
					local = buildCommunicationResponse([]communicationProcessResult{
						{participant: 0, request: participantReq, result: process},
						{participant: 1, request: participantReq, result: process},
						{manager: true, participant: -1, request: managerReq, result: process},
					}, communicationManagerResult{Verdict: "accepted", Score: &score}, nil, 2, 900, nil)
				}
				if local.Status != model.RunStatusAccepted || local.CPUTimeMs <= int64(req.Limits.TimeMs) {
					t.Fatalf("fixture did not produce an accepted aggregate above one role's limit: %+v", local)
				}
				if measured {
					local.WorkMeasurements = completePeerMeasurementResponse(t, req).WorkMeasurements
				}
				wire, err := json.Marshal(local)
				if err != nil {
					t.Fatal(err)
				}
				remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var forwarded model.RunRequest
					if err := json.NewDecoder(r.Body).Decode(&forwarded); err != nil {
						t.Error(err)
						http.Error(w, "invalid request", http.StatusBadRequest)
						return
					}
					if forwarded.WorkMeasurement != measured {
						t.Errorf("measurement flag changed during forwarding: %v", forwarded.WorkMeasurement)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: result\ndata: %s\n\n", wire)
				}))
				defer remote.Close()
				runner := &remoteRunner{client: remote.Client(), executeURL: remote.URL}
				got := runner.Run(context.Background(), req, Hooks{})
				if !reflect.DeepEqual(got, local) {
					t.Fatalf("peer verdict, accounting, or observations changed during SSE forwarding:\nlocal=%+v\nremote=%+v", local, got)
				}
			})
		}
	}
}
