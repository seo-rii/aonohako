package execute

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestAutomaticObservationRemotePreservesNativeResult(t *testing.T) {
	r := envelopeRequest()
	cfg := durante.Config{Observe: true, Families: []string{"cpp"}, CPUIDProfile: "x86-64-v2",
		ResourcePolicy: &durante.ResourcePolicy{MaxLimits: *r.WorkMeasurementLimits}}
	r.WorkMeasurement, r.IgnoreTLE, r.WorkMeasurementLimits = false, false, nil
	zero := 0
	primary := model.RunResponse{Status: model.RunStatusWA, ExitCode: &zero, VerdictSource: "stdout", CPUTimeMs: 7,
		TimeMs: 9, WallTimeMs: 9, CPUTimeNs: 7001000, MemoryKB: 1900, Stdout: "unchanged"}
	report := completeEnvelopeReport()
	report.CPUIDProfile = cfg.CPUIDProfile
	observation := model.WorkMeasurementObservation{Version: 1, State: "complete", OutputCheck: "native_output", Authority: durante.Authority, Report: report}
	for _, tc := range []struct {
		name   string
		update func(*model.RunResponse)
		state  string
	}{
		{"current", func(resp *model.RunResponse) {}, "complete"},
		{"older runner", func(resp *model.RunResponse) { resp.WorkMeasurementObservation = nil }, "unavailable"},
		{"unknown counter version", func(resp *model.RunResponse) {
			resp.WorkMeasurementObservation.Report.LatencyBound = &durante.LatencyBound{Version: 8, CyclesX16: 9000}
		}, "unavailable"},
		{"other processor", func(resp *model.RunResponse) { resp.WorkMeasurementObservation.Report.CPUIDProfile = "x86-64-v1" }, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := primary
			block := observation
			copyReport := *report
			block.Report = &copyReport
			wire.WorkMeasurementObservation = &block
			tc.update(&wire)
			got := exchangeTimeMeasurementResult(t, r, wire, cfg)
			side := got.WorkMeasurementObservation
			if side == nil || side.State != tc.state || (side.Report != nil) != (tc.state == "complete") {
				t.Fatalf("unexpected observation: %+v", side)
			}
			got.WorkMeasurementObservation = nil
			if !reflect.DeepEqual(got, primary) {
				t.Fatalf("native result changed: %+v", got)
			}
		})
	}
	r.StdinURL = "https://inputs.example/test"
	raw, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	if got := remoteObservation(r, primary, cfg, raw); got.State != "unavailable" {
		t.Fatalf("accepted missing shared URL digest: %+v", got)
	}
	observation.StdinSHA256 = strings.Repeat("a", 64)
	raw, err = json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	if got := remoteObservation(r, primary, cfg, raw); got.State != "complete" {
		t.Fatalf("rejected shared URL digest: %+v", got)
	}
	r.StdinURL = ""
	r.ExpectedStdout = "expected"
	tle := primary
	tle.Status, tle.VerdictSource, tle.ExitCode = model.RunStatusTLE, "cpu_time_final", nil
	observation.StdinSHA256 = ""
	raw, _ = json.Marshal(observation)
	if remoteObservation(r, tle, cfg, raw).State != "unavailable" {
		t.Fatal("accepted a native-output comparison against an incomplete native run")
	}
	observation.OutputCheck = "expected_output"
	raw, _ = json.Marshal(observation)
	if remoteObservation(r, tle, cfg, raw).State != "complete" {
		t.Fatal("rejected an answer-checked replay after a native CPU timeout")
	}
	tle.VerdictSource = "wall_time"
	if remoteObservation(r, tle, cfg, raw).State != "unavailable" {
		t.Fatal("accepted answer comparison for a wall timeout")
	}
	cfg.Observe = false
	if remoteObservation(r, primary, cfg, raw) != nil {
		t.Fatal("accepted unsolicited observation when automatic collection was disabled")
	}
}

func TestRemoteObservationCPUFeaturePolicy(t *testing.T) {
	cfg := observerConfig()
	req := observerRequest()
	zero := 0
	primary := model.RunResponse{Status: model.RunStatusAccepted, ExitCode: &zero, VerdictSource: "stdout", CPUTimeNs: 7001000, CPUTimeMs: 7}
	for _, tc := range []struct {
		name                string
		requested, reported []cpupolicy.Feature
		state               string
	}{
		{"missing", []cpupolicy.Feature{cpupolicy.AVX}, nil, "unavailable"},
		{"unexpected", nil, []cpupolicy.Feature{cpupolicy.AVX}, "unavailable"},
		{"different", []cpupolicy.Feature{cpupolicy.AVX}, []cpupolicy.Feature{cpupolicy.SIMD}, "unavailable"},
		{"canonical", []cpupolicy.Feature{cpupolicy.SIMD, cpupolicy.AVX}, []cpupolicy.Feature{cpupolicy.AVX, cpupolicy.SIMD}, "complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req.DisallowedCPUFeatures = tc.requested
			report := completeEnvelopeReport()
			report.CPUIDProfile = cfg.CPUIDProfile
			report.DisallowedCPUFeatures = tc.reported
			wire := primary
			wire.WorkMeasurementObservation = &model.WorkMeasurementObservation{
				Version: 1, Authority: durante.Authority, State: "complete", OutputCheck: "native_output", Report: report,
			}
			got := exchangeTimeMeasurementResult(t, req, wire, cfg)
			observation := got.WorkMeasurementObservation
			if observation == nil || observation.State != tc.state || (observation.Report != nil) != (tc.state == "complete") {
				t.Fatalf("unexpected observation: %+v", observation)
			}
			got.WorkMeasurementObservation = nil
			if !reflect.DeepEqual(got, primary) {
				t.Fatalf("primary verdict or timing changed: %+v", got)
			}
		})
	}
}

func TestRemotePipelineObservationsBindRowsToNativeSteps(t *testing.T) {
	cfg := observerConfig()
	ordinary := observerRequest()
	step := model.PipelineStep{ID: "produce", Limits: ordinary.Limits,
		Executor: model.PipelineExecutor{Kind: "batch", ProgramID: "p"}}
	second := step
	second.ID = "consume"
	req := &model.RunRequest{Pipeline: &model.PipelineV1{
		Version: 1, Programs: []model.RunProgram{{ID: "p", Lang: ordinary.Lang, Binaries: ordinary.Binaries}},
		Steps: []model.PipelineStep{step, second},
	}}
	zero := 0
	native := model.StepResult{ID: step.ID, ProgramID: "p", Status: model.RunStatusAccepted, ExitCode: &zero}
	primary := model.RunResponse{Status: model.RunStatusWA, VerdictSource: "final:diff", Steps: []model.StepResult{native, native}}
	primary.Steps[1].ID = second.ID
	report := completeEnvelopeReport()
	report.CPUIDProfile = cfg.CPUIDProfile
	firstRow := model.PipelineStepObservation{StepID: step.ID, ProgramID: "p", State: "complete", OutputCheck: "native_output", Report: report}
	lastRow := firstRow
	lastRow.StepID = second.ID
	block := model.WorkMeasurementObservation{Version: 2, State: "pipeline", Authority: durante.Authority,
		Steps: []model.PipelineStepObservation{firstRow, lastRow}}
	get := func(block model.WorkMeasurementObservation) *model.WorkMeasurementObservation {
		t.Helper()
		raw, err := json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		return remoteObservation(req, primary, cfg, raw)
	}
	if got := get(block); got.State != "pipeline" || !reflect.DeepEqual(got.Steps, block.Steps) {
		t.Fatalf("compatible pipeline rows not returned: %+v", got)
	}
	wrong := block
	wrong.Steps = []model.PipelineStepObservation{lastRow, firstRow}
	if get(wrong).State != "unavailable" || remoteObservation(req, primary, cfg, nil).State != "unavailable" ||
		remoteObservation(req, primary, cfg, json.RawMessage(`{"version":2,"steps":"corrupt"}`)).State != "unavailable" {
		t.Fatal("missing, malformed or swapped steps accepted")
	}
	unknown := block
	unknown.Steps = []model.PipelineStepObservation{firstRow, {StepID: second.ID, ProgramID: "p", State: "unavailable", Reason: "PRIVATE: file payload"}}
	got := get(unknown)
	if got.Steps[1].State != "unavailable" || strings.Contains(got.Steps[1].Reason, "PRIVATE") || got.Steps[0].State != "complete" {
		t.Fatalf("unavailable reason crossed the pipeline boundary: %+v", got.Steps)
	}
	primary.VerdictSource = "step:produce:artifact"
	if get(block).State != "unavailable" {
		t.Fatal("accepted rows after a failed artifact handoff")
	}
	primary.Steps[0].Status = model.RunStatusRE
	primary.VerdictSource = "step:produce:exit_code"
	if get(block).State != "unavailable" {
		t.Fatal("accepted rows after a failed native stage")
	}
	primary.Steps[0].Status = model.RunStatusAccepted
	primary.VerdictSource = "step:consume:exit_code"
	primary.Steps[1].Status = model.RunStatusRE
	if got := get(block); got.Steps[1].Report != nil || got.Steps[0].State != "complete" {
		t.Fatal("accepted a complete report for a failed native stage")
	}
	oneBudget := remoteObservationBudget(ordinary, cfg)
	if oneBudget <= 0 || remoteObservationBudget(req, cfg) != 2*oneBudget {
		t.Fatal("deadline omits a bounded stage replay")
	}
	req.Pipeline.Steps[1].Executor = model.PipelineExecutor{Kind: "interactive", ParticipantProgramID: "p", InteractorProgramID: "judge"}
	if remoteObservationBudget(req, cfg) != 2*oneBudget {
		t.Fatal("deadline omits the eligible interactive contestant")
	}
	primary.Status, primary.VerdictSource = model.RunStatusAccepted, "final:diff"
	nativeRole := model.PipelineRoleCPU{ProgramID: "p", ExitCode: &zero, Status: model.RunStatusAccepted}
	judgeRole := nativeRole
	judgeRole.ProgramID = "judge"
	primary.Steps[1].Status, primary.Steps[1].VerdictSource = model.RunStatusAccepted, "interactor"
	primary.Steps[1].CPUTimeByRole = &model.PipelineRoleTimes{Contestant: nativeRole, Interactor: judgeRole}
	if got := get(block); got.Steps[1].State != "unavailable" {
		t.Fatal("accepted ordinary batch output evidence for an interactive exchange")
	}
	block.Steps[1].OutputCheck = "interactive_transcript"
	if got := get(block); got.Steps[1].State != "complete" {
		t.Fatal("rejected a verified contestant exchange")
	}
	reject := 1
	primary.Status, primary.VerdictSource = model.RunStatusWA, "step:consume:interactor:exit_code"
	primary.Steps[1].Status, primary.Steps[1].VerdictSource = model.RunStatusWA, "interactor:exit_code"
	primary.Steps[1].CPUTimeByRole.Interactor.Status = model.RunStatusWA
	primary.Steps[1].CPUTimeByRole.Interactor.ExitCode = &reject
	if got := get(block); got.Steps[1].State != "complete" {
		t.Fatal("rejected a complete exchange that received native WA")
	}
	req.Pipeline.Steps[1].Executor.InteractorLimits = &model.Limits{TimeMs: 1}
	primary.Steps[1].CPUTimeByRole.Interactor.CPUTimeMs = 2
	if got := get(block); got.Steps[1].State != "unavailable" || got.Steps[0].State != "complete" {
		t.Fatal("credited an exchange whose judge exceeded its CPU limit")
	}
	primary.Steps[1].CPUTimeByRole.Interactor.CPUTimeMs = 0
	primary.Steps[1].CPUTimeByRole.Contestant.ExitCode = nil
	if got := get(block); got.Steps[1].State != "unavailable" {
		t.Fatal("credited an interrupted interactive contestant")
	}
	cfg.Observe = false
	if remoteObservationBudget(req, cfg) != 0 || remoteObservation(req, primary, cfg, nil) != nil {
		t.Fatal("unsolicited pipeline observation")
	}
}
