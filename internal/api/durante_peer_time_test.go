package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/execute"
	"aonohako/internal/model"
)

func apiPeerTimeRequest(mode string) *model.RunRequest {
	hash := sha256.Sum256([]byte("x"))
	artifact := model.Binary{Name: "main", DataB64: "eA==", Mode: "exec", SHA256: hex.EncodeToString(hash[:])}
	req := &model.RunRequest{Lang: "CPP20", WorkMeasurement: true, Binaries: []model.Binary{artifact}, Limits: model.Limits{TimeMs: 1000, MemoryMB: 64},
		Interactor: &model.InteractorSpec{Lang: "CPP20", Binaries: []model.Binary{artifact}},
		WorkMeasurementPeerTime: &model.PeerTimeRequest{Version: 1, Budgets: []model.PeerTimeBudget{
			{Role: "contestant", ProgramID: "contestant", TimeMs: 1},
			{Role: "interactor", ProgramID: "interactor", TimeMs: 2},
		}},
	}
	if mode == "interactive" {
		return req
	}
	req.Lang, req.Binaries, req.Interactor = "", nil, nil
	req.Programs = []model.RunProgram{
		{ID: "solver", Lang: "CPP20", Binaries: []model.Binary{artifact}, SourceSHA256: strings.Repeat("a", 64)},
		{ID: "judge", Lang: "CPP20", Binaries: []model.Binary{artifact}, SourceSHA256: strings.Repeat("b", 64)},
	}
	req.Communication = &model.CommunicationSpec{Version: 1, ParticipantCount: 2, ParticipantProgramID: "solver", ManagerProgramID: "judge", ResultProtocol: "manager-result-v1"}
	first, second := 0, 1
	req.WorkMeasurementPeerTime.Budgets = []model.PeerTimeBudget{
		{Role: "participant", ProgramID: "solver", ParticipantIndex: &first, TimeMs: 1},
		{Role: "participant", ProgramID: "solver", ParticipantIndex: &second, TimeMs: 2},
		{Role: "manager", ProgramID: "judge", TimeMs: 3},
	}
	if mode == "communication-v2" {
		other := req.Programs[0]
		other.ID = "other"
		req.Programs = append(req.Programs, other)
		empty := sha256.Sum256(nil)
		req.Communication = &model.CommunicationSpec{Version: 2, ParticipantCount: 2, ParticipantProgramIDs: []string{"solver", "other"}, ManagerProgramID: "judge", ResultProtocol: "match-result-v1", InputSHA256: hex.EncodeToString(empty[:]), RuntimeFingerprint: strings.Repeat("c", 64)}
		req.WorkMeasurementPeerTime.Budgets[1].ProgramID = "other"
	}
	return req
}

func TestPeerTimeHTTPAndSSEPreserveRoleBudgets(t *testing.T) {
	for _, mode := range []string{"interactive", "communication-v1", "communication-v2"} {
		t.Run(mode, func(t *testing.T) {
			req := apiPeerTimeRequest(mode)
			original := model.RunResponse{Status: model.RunStatusRE, Reason: "unstarted test roles", CPUTimeNs: 123, MemoryKB: 456}
			for _, budget := range req.WorkMeasurementPeerTime.Budgets {
				original.WorkMeasurements = append(original.WorkMeasurements, model.WorkMeasurementProcess{
					Role: budget.Role, ProgramID: budget.ProgramID, ParticipantIndex: budget.ParticipantIndex,
					ExecutionStatus: model.RunStatusInitFail,
					TimeBudget:      &model.PeerTimeBudgetResult{Version: 1, Authority: durante.Authority, BudgetMs: budget.TimeMs, Unavailable: "execution_not_complete"},
				})
			}
			budgets := req.WorkMeasurementPeerTime.Budgets
			budgets[0], budgets[len(budgets)-1] = budgets[len(budgets)-1], budgets[0]
			var calls atomic.Int64
			server := NewWithServices(configForTest(t), compileRunnerStub{}, executeRunnerStub{run: func(_ context.Context, forwarded *model.RunRequest, _ execute.Hooks) model.RunResponse {
				calls.Add(1)
				if !reflect.DeepEqual(forwarded.WorkMeasurementPeerTime, req.WorkMeasurementPeerTime) {
					t.Error("HTTP decoder changed the role identities, seats, budgets or array order")
				}
				return original
			}})
			remote := httptest.NewServer(server.Handler())
			defer remote.Close()
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.Post(remote.URL+"/execute", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("HTTP admission rejected valid peer budgets: status=%d", response.StatusCode)
			}
			events := readSSEEvents(response.Body, t)
			if calls.Load() != 1 || len(events) == 0 || events[len(events)-1].Name != "result" {
				t.Fatalf("missing runner result: %+v", events)
			}
			wire, err := json.Marshal(events[len(events)-1].JSON)
			if err != nil {
				t.Fatal(err)
			}
			var got model.RunResponse
			if err := json.Unmarshal(wire, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, original) {
				t.Fatalf("SSE altered role budgets or original result: %+v", got)
			}
		})
	}
}

func TestPeerTimeHTTPRejectsInvalidRequestBeforeExecution(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"no measurement":      func(r map[string]any) { r["work_measurement"] = false },
		"ordinary time mixed": func(r map[string]any) { r["work_measurement_time"] = true },
		"unknown version":     func(r map[string]any) { r["work_measurement_peer_time"].(map[string]any)["version"] = 2 },
		"caller coefficients": func(r map[string]any) {
			r["work_measurement_peer_time"].(map[string]any)["coefficients"] = map[string]any{"instruction_ns_numerator": 0}
		},
	} {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(apiPeerTimeRequest("interactive"))
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			edit(payload)
			body, _ = json.Marshal(payload)
			called := false
			server := NewWithServices(configForTest(t), compileRunnerStub{}, executeRunnerStub{run: func(context.Context, *model.RunRequest, execute.Hooks) model.RunResponse {
				called = true
				return model.RunResponse{}
			}})
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body)))
			if recorder.Code != http.StatusBadRequest || called || strings.Contains(recorder.Header().Get("Content-Type"), "event-stream") {
				t.Fatalf("invalid request entered execution/SSE: status=%d called=%v body=%s", recorder.Code, called, recorder.Body.String())
			}
		})
	}
}
