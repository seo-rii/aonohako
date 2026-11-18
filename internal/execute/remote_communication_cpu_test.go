package execute

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/timing"
)

func communicationCPUWireFixture(version int) (*model.RunRequest, model.RunResponse) {
	req := competitiveRequest()
	if version == 1 {
		req.Communication.Version = 1
		req.Communication.ParticipantProgramIDs = nil
		req.Communication.ParticipantProgramID = "seat0"
	}
	processes := competitiveProcesses(time.Now())
	for i := range processes {
		process := &processes[i]
		process.started = true
		limits := req.Limits
		if process.manager {
			limits = communicationManagerLimits(limits)
		}
		process.request = &model.RunRequest{Limits: limits}
		raw := uint64(i+1)*1_000_000 + 1
		rawMs := ceilCPUMilliseconds(raw)
		process.result.RawCPUTimeNs, process.result.RawCPUTimeMs = &raw, &rawMs
		process.result.CPUTimeNs = raw * 2
		process.result.CPUTimeMs = ceilCPUMilliseconds(raw * 2)
	}
	var native model.RunResponse
	if version == 2 {
		result := &communicationMatchManagerResult{Version: 1, Games: 20, Wins: []int{11, 9}}
		native = buildCommunicationMatchResponse(req, processes, result, nil, 2, 28, nil, time.Time{}, nil)
	} else {
		score := 0.625
		native = buildCommunicationResponse(processes, communicationManagerResult{Verdict: "accepted", Score: &score}, nil, 2, 28, nil)
	}
	attachCommunicationRoleTiming(req, &native, processes)
	native.CPUTimeNormalization = &model.CPUTimeNormalization{Method: timing.CPUNormalizationMethod, ReferenceTimeNs: 2, ObservedTimeNs: 1, ScalePPM: 2_000_000}
	return req, native
}

func forwardCommunicationCPU(t *testing.T, req *model.RunRequest, native model.RunResponse, rows json.RawMessage) model.RunResponse {
	t.Helper()
	wire, err := json.Marshal(struct {
		model.RunResponse
		Rows json.RawMessage `json:"communication_cpu_by_role,omitempty"`
	}{native, rows})
	if err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: result\ndata: "))
		w.Write(wire)
		w.Write([]byte("\n\n"))
	}))
	defer remote.Close()
	runner := &remoteRunner{client: remote.Client(), executeURL: remote.URL}
	return runner.Run(t.Context(), req, Hooks{})
}

func TestRemoteCommunicationCPUForwarding(t *testing.T) {
	missingSeat := func(r *model.RunResponse) {
		row := &r.CommunicationCPUByRole[1]
		r.Status, r.VerdictSource, r.StartedParticipants, r.Score = model.RunStatusRE, "communication:startup", 1, nil
		r.CPUTimeMs -= row.CPUTimeMs
		r.CPUTimeNs -= *row.CPUTimeNs
		*r.RawCPUTimeMs -= *row.RawCPUTimeMs
		*r.RawCPUTimeNs -= *row.RawCPUTimeNs
		row.Started, row.Interrupted, row.ExecutionStatus, row.ExitCode = false, true, model.RunStatusInitFail, nil
		row.CPUTimeMs, row.CPUTimeNs, row.RawCPUTimeMs, row.RawCPUTimeNs = 0, nil, nil, nil
	}
	for _, tc := range []struct {
		name    string
		version int
		mutate  func(*model.RunResponse)
		valid   bool
	}{
		{"v1 duplicate program IDs", 1, nil, true},
		{"v2 seats and manager", 2, nil, true},
		{"older worker", 1, func(r *model.RunResponse) { r.CommunicationCPUByRole = nil }, false},
		{"v1 swapped seats", 1, func(r *model.RunResponse) {
			rows := r.CommunicationCPUByRole
			rows[0], rows[1] = rows[1], rows[0]
		}, false},
		{"v2 wrong program", 2, func(r *model.RunResponse) { r.CommunicationCPUByRole[1].ProgramID = "seat0" }, false},
		{"wrong manager budget", 2, func(r *model.RunResponse) { r.CommunicationCPUByRole[2].CPULimitMs-- }, false},
		{"wrong participant budget", 1, func(r *model.RunResponse) { r.CommunicationCPUByRole[0].CPULimitMs++ }, false},
		{"wrong role unit", 1, func(r *model.RunResponse) { r.CommunicationCPUByRole[0].CPUTimeMs++ }, false},
		{"v2 mismatched seat CPU", 2, func(r *model.RunResponse) { r.Match.Participants[1].CPUTimeMs++ }, false},
		{"wrong normalized total", 1, func(r *model.RunResponse) { r.CPUTimeNs++ }, false},
		{"wrong raw total", 2, func(r *model.RunResponse) { (*r.RawCPUTimeNs)++ }, false},
		{"wrong calibration", 1, func(r *model.RunResponse) { r.CPUTimeNormalization.ScalePPM++ }, false},
		{"wrong raw to normalized conversion", 1, func(r *model.RunResponse) {
			(*r.CommunicationCPUByRole[0].RawCPUTimeNs)++
			(*r.RawCPUTimeNs)++
		}, false},
		{"wrong start count", 1, func(r *model.RunResponse) { r.StartedParticipants-- }, false},
		{"raw sum overflow", 1, func(r *model.RunResponse) {
			r.CPUTimeNormalization = &model.CPUTimeNormalization{Method: timing.CPUNormalizationMethod, ReferenceTimeNs: 1, ObservedTimeNs: math.MaxUint64, ScalePPM: 1}
			r.CPUTimeMs, r.CPUTimeNs, r.RawCPUTimeNs = 3, 3, nil
			raw, normalized := uint64(math.MaxUint64), uint64(1)
			rawMs := ceilCPUMilliseconds(raw)
			sumMs := rawMs * 3
			r.RawCPUTimeMs = &sumMs
			for i := range r.CommunicationCPUByRole {
				row := &r.CommunicationCPUByRole[i]
				row.RawCPUTimeMs, row.RawCPUTimeNs, row.CPUTimeMs, row.CPUTimeNs = &rawMs, &raw, 1, &normalized
			}
		}, false},
		{"started but missing exact sample", 1, func(r *model.RunResponse) {
			row := &r.CommunicationCPUByRole[1]
			row.CPUTimeNs, row.RawCPUTimeNs = nil, nil
			// The sandbox falls back to normalizing the raw millisecond sample.
			r.CPUTimeMs -= row.CPUTimeMs
			row.CPUTimeMs = *row.RawCPUTimeMs * 2
			r.CPUTimeMs += row.CPUTimeMs
			r.CPUTimeNs, r.RawCPUTimeNs = 0, nil
		}, true},
		{"unstarted and interrupted", 1, missingSeat, true},
		{"unstarted with exit status", 1, func(r *model.RunResponse) {
			missingSeat(r)
			zero := 0
			r.CommunicationCPUByRole[1].ExitCode = &zero
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, want := communicationCPUWireFixture(tc.version)
			if tc.mutate != nil {
				tc.mutate(&want)
			}
			var wire json.RawMessage
			if want.CommunicationCPUByRole != nil {
				data, err := json.Marshal(want.CommunicationCPUByRole)
				if err != nil {
					t.Fatal(err)
				}
				// The returned public role record must only expose known timing
				// fields, even if a future worker adds private diagnostic text.
				wire = bytes.Replace(data, []byte(`"role":`), []byte(`"worker_private":"private manager output","role":`), 1)
			}
			got := forwardCommunicationCPU(t, req, want, wire)
			if !tc.valid {
				want.CommunicationCPUByRole, want.CPUTimeNs, want.RawCPUTimeNs = nil, 0, nil
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("forwarded response = %+v, want %+v", got, want)
			}
		})
	}
}

func TestRemoteCommunicationCPURejectsMalformedOptionalJSON(t *testing.T) {
	req, native := communicationCPUWireFixture(2)
	rows, err := json.Marshal(native.CommunicationCPUByRole)
	if err != nil {
		t.Fatal(err)
	}
	want := native
	want.CommunicationCPUByRole, want.CPUTimeNs, want.RawCPUTimeNs = nil, 0, nil
	for _, tc := range []struct {
		name string
		wire json.RawMessage
	}{
		{"incompatible shape", json.RawMessage(`{"private":"future worker format"}`)},
		{"missing interruption flag", bytes.Replace(rows, []byte(`"interrupted":false,`), nil, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := forwardCommunicationCPU(t, req, native, tc.wire)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("malformed optional record changed native match: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestRemoteIgnoresUnrequestedCommunicationRoleCPU(t *testing.T) {
	req := &model.RunRequest{Lang: "CPP20", Limits: model.Limits{TimeMs: 100}}
	native := model.RunResponse{Status: model.RunStatusAccepted, VerdictSource: "stdout", CPUTimeMs: 2, CPUTimeNs: 1_200_000}
	got := forwardCommunicationCPU(t, req, native, json.RawMessage(`[{"role":"manager","program_id":"unrequested"}]`))
	if !reflect.DeepEqual(got, native) {
		t.Fatalf("ordinary response had unsolicited communication data or lost CPU precision: %+v", got)
	}
}
