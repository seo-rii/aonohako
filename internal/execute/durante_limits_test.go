package execute

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func envelopeRequest() *model.RunRequest {
	return &model.RunRequest{Lang: "CPP20", WorkMeasurement: true, IgnoreTLE: true,
		Limits:                model.Limits{TimeMs: 10, WallTimeMs: 20, MemoryMB: 16, OutputBytes: 1024, WorkspaceBytes: 65536},
		WorkMeasurementLimits: &durante.ResourceLimits{CPUTimeMs: 3000, WallTimeMs: 10000, MemoryMB: 512}}
}
func completeEnvelopeReport() *durante.Report {
	instructions, zero := uint64(1234), uint64(0)
	return &durante.Report{Schema: 1, ClientVersion: 1, Complete: true, Quality: "uninterrupted", Authority: durante.Authority, Metric: durante.Metric, Scope: durante.Scope, REPEnabled: true, EngineProfile: durante.EngineProfile, Family: "cpp", DispatchProfile: durante.DispatchDefault, Instructions: &instructions, REPBytes: &zero, REPEntries: &zero, UnsupportedREPEntries: &zero, Processes: 1, Images: 1}
}
func TestWorkMeasurementRawRequestIsolated(t *testing.T) {
	req := envelopeRequest()
	before := *req
	hardBefore := *req.WorkMeasurementLimits
	raw := workMeasurementRawRequest(req)
	if raw.Limits.TimeMs != 3000 || raw.Limits.WallTimeMs != 10000 || raw.Limits.MemoryMB != 512 || raw.IgnoreTLE || raw.Limits.OutputBytes != 1024 || raw.Limits.WorkspaceBytes != 65536 {
		t.Fatalf("wrong raw envelope: %+v", raw)
	}
	raw.WorkMeasurementLimits.MemoryMB = 128
	if !reflect.DeepEqual(*req, before) || *req.WorkMeasurementLimits != hardBefore {
		t.Fatal("modified caller's request")
	}
}
func TestWorkMeasurementFinishNeverGrantsVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, status, state string
		valid, cancel       bool
	}{
		{"completed", model.RunStatusAccepted, "complete", true, false},
		{"wrong output", model.RunStatusWA, "complete", true, false},
		{"missing count", model.RunStatusAccepted, "incomplete", false, false},
		{"cpu abort with forged complete count", model.RunStatusTLE, "resource_exhausted", true, false},
		{"memory abort", model.RunStatusMLE, "resource_exhausted", true, false},
		{"cancellation", model.RunStatusTLE, "cancelled", true, true},
		{"runtime error", model.RunStatusRE, "failed", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			exit := 0
			score := 1.0
			resp := model.RunResponse{Status: tc.status, CPUTimeNs: 12000000, CPUTimeMs: 12, MemoryKB: 32000, ExitCode: &exit, Score: &score, VerdictSource: "stdout"}
			if tc.valid {
				resp.WorkMeasurement = completeEnvelopeReport()
			}
			got := finishWorkMeasurementDiagnostic(ctx, envelopeRequest(), resp)
			if got.Status != model.RunStatusInitFail || got.Score != nil || got.WorkMeasurementExecution.State != tc.state || got.CPUTimeNs != resp.CPUTimeNs || got.MemoryKB != resp.MemoryKB {
				t.Fatalf("unsafe result: %+v", got)
			}
			if err := validateRemoteWorkMeasurement(envelopeRequest(), got); err != nil {
				t.Fatal(err)
			}
			if tc.state != "complete" && got.WorkMeasurementExecution.OutputMatches != nil {
				t.Fatal("aborted run claims output match")
			}
		})
	}
}
func TestWorkMeasurementRemoteRejectsLegacyAndInconsistentResults(t *testing.T) {
	req := envelopeRequest()
	exit := 0
	good := finishWorkMeasurementDiagnostic(context.Background(), req, model.RunResponse{Status: model.RunStatusAccepted, ExitCode: &exit, WorkMeasurement: completeEnvelopeReport(), VerdictSource: "stdout"})
	for _, change := range []func(*model.RunResponse){
		func(r *model.RunResponse) { r.Status = model.RunStatusAccepted },
		func(r *model.RunResponse) { v := 1.; r.Score = &v },
		func(r *model.RunResponse) { r.CPUTimeNormalization = &model.CPUTimeNormalization{ScalePPM: 1000000} },
		func(r *model.RunResponse) { r.WorkMeasurementExecution.RawLimits.MemoryMB++ },
		func(r *model.RunResponse) { r.WorkMeasurementExecution.NominalLimits.TimeMs++ },
		func(r *model.RunResponse) { r.WorkMeasurementExecution.State = "future" },
		func(r *model.RunResponse) { r.WorkMeasurementExecution.TerminationSource = "cpu_time" },
		func(r *model.RunResponse) { r.WorkMeasurement = nil },
		func(r *model.RunResponse) { r.ExitCode = nil },
	} {
		r := good
		d := *good.WorkMeasurementExecution
		r.WorkMeasurementExecution = &d
		change(&r)
		if validateRemoteWorkMeasurement(req, r) == nil {
			t.Fatalf("accepted malformed diagnostic %+v", r)
		}
	}
	if validateRemoteWorkMeasurement(&model.RunRequest{}, good) == nil {
		t.Fatal("accepted unsolicited diagnostic")
	}
}
func TestWorkMeasurementRemoteReservesRawWallOnly(t *testing.T) {
	req := envelopeRequest()
	req.WorkMeasurementLimits.WallTimeMs = 120000
	got := remoteExecutionTimeout(req, time.Second)
	ordinary := *req
	ordinary.WorkMeasurementLimits = nil
	ordinary.WorkMeasurement = false
	ordinary.Limits.TimeMs = 120000
	ordinary.Limits.WallTimeMs = 120000
	want := remoteExecutionTimeout(&ordinary, time.Second)
	if got != want {
		t.Fatalf("raw wall not reserved: got %s want %s", got, want)
	}
}
func TestWorkMeasurementRejectsResourcePolicyBeforeExecution(t *testing.T) {
	cfg, _ := durante.ParseConfig("cpp", "", "", "")
	svc := NewWithConfig(config.Config{Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	got := svc.Run(context.Background(), envelopeRequest(), Hooks{})
	if got.Status != model.RunStatusInitFail || got.Reason == "no binaries" {
		t.Fatalf("disabled policy reached execution: %+v", got)
	}
}

func TestWorkMeasurementRejectsMalformedCompleteReports(t *testing.T) {
	for name, edit := range map[string]func(*durante.Report){
		"zero":     func(r *durante.Report) { v := uint64(0); r.Instructions = &v },
		"reason":   func(r *durante.Report) { r.Reason = "failed" },
		"rep":      func(r *durante.Report) { v := uint64(1); r.REPBytes = &v },
		"family":   func(r *durante.Report) { r.Family = "python" },
		"engine":   func(r *durante.Report) { r.EngineProfile = "other" },
		"dispatch": func(r *durante.Report) { r.DispatchProfile = "other" },
		"signals":  func(r *durante.Report) { r.SignalsObserved = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			r := completeEnvelopeReport()
			edit(r)
			if validDiagnosticWorkReport("CPP20", r) {
				t.Fatal("accepted malformed complete report")
			}
		})
	}
}

func TestWorkMeasurementServiceFreezesOperatorAuthorization(t *testing.T) {
	cfg, err := durante.ParseConfig("cpp", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg.ResourcePolicy, err = durante.ParseResourcePolicy(`{"max_limits":{"cpu_time_ms":3000,"wall_time_ms":10000,"memory_mb":512},"memory_budget_mb":4096,"reserved_memory_mb":512}`)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWithConfig(config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	cfg.Families[0] = "disabled"
	cfg.ResourcePolicy.MaxLimits.CPUTimeMs = 5000
	req := envelopeRequest()
	req.WorkMeasurementLimits.CPUTimeMs = 4000
	r := svc.Run(context.Background(), req, Hooks{})
	if r.Status != model.RunStatusInitFail || !strings.Contains(r.Reason, "operator max_limits") {
		t.Fatalf("operator cap changed after construction: %+v", r)
	}
	req.WorkMeasurementLimits.CPUTimeMs = 3000
	r = svc.Run(context.Background(), req, Hooks{})
	if r.Reason != "work measurement failed: no binaries" || r.WorkMeasurementExecution == nil {
		t.Fatalf("authorization changed after construction: %+v", r)
	}
}

func TestWorkMeasurementEnvelopeBindsRuntimeFamily(t *testing.T) {
	for _, tc := range []struct{ language, family string }{{"CPP20", "cpp"}, {"PYTHON3", "cpython"}, {"PYPY3", "pypy"}, {"RUST2024", "native"}, {"JAVASCRIPT", "javascript-wasm"}} {
		t.Run(tc.family, func(t *testing.T) {
			req := envelopeRequest()
			req.Lang = tc.language
			report := completeEnvelopeReport()
			report.Family = tc.family
			exit := 0
			response := finishWorkMeasurementDiagnostic(context.Background(), req, model.RunResponse{Status: model.RunStatusAccepted, ExitCode: &exit, VerdictSource: "stdout", WorkMeasurement: report})
			if response.WorkMeasurementExecution.State != "complete" || response.Status != model.RunStatusInitFail || response.Score != nil {
				t.Fatalf("family diagnostic failed: %+v", response)
			}
			if err := validateRemoteWorkMeasurement(req, response); err != nil {
				t.Fatal(err)
			}
			response.WorkMeasurement.Family = "cpp"
			if tc.family == "cpp" {
				response.WorkMeasurement.Family = "cpython"
			}
			if err := validateRemoteWorkMeasurement(req, response); err == nil {
				t.Fatal("accepted another runtime family's counts")
			}
		})
	}
	for _, language := range []string{"PYTHON3", "PYPY3"} {
		report := completeEnvelopeReport()
		report.Family, _ = durante.FamilyForLanguage(language)
		report.DispatchProfile = durante.DispatchGLIBC241
		report.HostLibc = "2.41"
		if validDiagnosticWorkReport(language, report) {
			t.Fatal("accepted C/C++ dispatch for interpreter")
		}
	}
	report := completeEnvelopeReport()
	report.Family = "jvm"
	if validDiagnosticWorkReport("JAVA17", report) {
		t.Fatal("known incompatible JVM accepted")
	}
}
