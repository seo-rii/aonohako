package execute

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// The unit model is deliberately synthetic, not a production calibration.
func timeMeasurementConfig() durante.Config {
	image := "sha256:" + strings.Repeat("a", 64)
	return durante.Config{
		Families: []string{"cpp"}, Drrun: durante.DefaultDrrun, Client: durante.DefaultClient,
		DispatchProfile: durante.DispatchDefault, RuntimeImage: image,
		ResourcePolicy: &durante.ResourcePolicy{
			MaxLimits:      durante.ResourceLimits{CPUTimeMs: 3000, WallTimeMs: 10000, MemoryMB: 512},
			MemoryBudgetMB: 4096, ReservedMemoryMB: 512,
		},
		TimePolicy: &durante.TimePolicy{
			ModelID: "unit-synthetic", ModelVersion: 1, ReferenceID: "unit-only",
			RuntimeID: "CPP20@" + image, Family: "cpp", EngineProfile: durante.EngineProfile,
			DispatchProfile:        durante.DispatchDefault,
			InstructionNSNumerator: 1, REPByteNSNumerator: 1, NSDenominator: 1,
		},
	}
}

func timeMeasurementRequest() *model.RunRequest {
	req := envelopeRequest()
	req.WorkMeasurementTime = true
	return req
}

func timeMeasurementDiagnostic(req *model.RunRequest, status string, report *durante.Report) model.RunResponse {
	exit := 0
	return finishWorkMeasurementDiagnostic(context.Background(), req, model.RunResponse{
		Status: status, ExitCode: &exit, VerdictSource: "stdout", WorkMeasurement: report,
		// Raw execution may exceed the nominal limits while staying inside its
		// diagnostic envelope. Conversion must not rewrite these observations.
		CPUTimeMs: 12, CPUTimeNs: 12_000_000, ProcessCPUTimeMs: 11,
		TimeMs: 20, WallTimeMs: 20, MemoryKB: 32000,
	})
}

func TestWorkMeasurementTimeBoundaryPreservesDiagnosticResult(t *testing.T) {
	for _, tc := range []struct {
		name              string
		status            string
		instructions, rep uint64
		wantNS, wantMS    int64
		wantExceeded      bool
	}{
		{"below", model.RunStatusAccepted, 9_999_999, 0, 9_999_999, 10, false},
		{"equal", model.RunStatusAccepted, 10_000_000, 0, 10_000_000, 10, false},
		{"REP crosses by one nanosecond", model.RunStatusAccepted, 9_999_999, 2, 10_000_001, 11, true},
		{"wrong output", model.RunStatusWA, 10_000_000, 0, 10_000_000, 10, false},
		{"above float precision", model.RunStatusAccepted, 1<<53 + 1, 2, 9_007_199_254_740_995, 9_007_199_255, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, cfg := timeMeasurementRequest(), timeMeasurementConfig()
			report := completeEnvelopeReport()
			report.Instructions, report.REPBytes = &tc.instructions, &tc.rep
			if tc.rep != 0 {
				entries := uint64(1)
				report.REPEntries = &entries
			}
			before := timeMeasurementDiagnostic(req, tc.status, report)
			beforeExecution := *before.WorkMeasurementExecution
			got := finishWorkMeasurementTime(req, before, cfg)
			d := got.WorkMeasurementExecution
			if d.TimeEstimate == nil || d.NominalTimeExceeded == nil || d.TimeEstimateUnavailable != "" {
				t.Fatalf("missing successful estimate: %+v", d)
			}
			if d.TimeEstimate.Nanoseconds != tc.wantNS || d.TimeEstimate.Milliseconds != tc.wantMS || *d.NominalTimeExceeded != tc.wantExceeded {
				t.Fatalf("estimate = %+v, exceeded = %v; want %d ns, %d ms, exceeded %t", d.TimeEstimate, *d.NominalTimeExceeded, tc.wantNS, tc.wantMS, tc.wantExceeded)
			}
			if d.TimeEstimate.Authority != durante.Authority || d.TimeEstimate.RuntimeID != cfg.TimePolicy.RuntimeID || !report.ValidDiagnosticContract() {
				t.Fatalf("conversion changed identity or diagnostic authority: %+v", d.TimeEstimate)
			}
			if d.OutputMatches == nil || *d.OutputMatches != (tc.status == model.RunStatusAccepted) {
				t.Fatalf("conversion changed the output comparison: %+v", d)
			}
			if d == before.WorkMeasurementExecution || !reflect.DeepEqual(*before.WorkMeasurementExecution, beforeExecution) {
				t.Fatal("conversion mutated the input envelope")
			}
			withoutTime := *d
			withoutTime.TimeEstimate, withoutTime.NominalTimeExceeded, withoutTime.TimeEstimateUnavailable = nil, nil, ""
			withoutTime.TimeEstimateEligibility = nil
			got.WorkMeasurementExecution = &withoutTime
			if !reflect.DeepEqual(got, before) {
				t.Fatalf("conversion changed execution state, raw observations, output, or verdict:\nbefore=%+v\nafter=%+v", before, got)
			}
		})
	}
}

func TestWorkMeasurementTimeNeverEstimatesInterruptedExecution(t *testing.T) {
	for _, tc := range []struct {
		name, status, source, state string
		cancel, missingReport       bool
		exit                        int
	}{
		{name: "raw CPU cap", status: model.RunStatusTLE, source: "cpu_time_cgroup", state: "resource_exhausted"},
		{name: "raw wall cap", status: model.RunStatusTLE, source: "wall_time", state: "resource_exhausted"},
		{name: "raw memory cap", status: model.RunStatusMLE, source: "memory_cgroup", state: "resource_exhausted"},
		{name: "workspace cap", status: model.RunStatusWLE, source: "workspace", state: "failed"},
		{name: "cancel after collection", status: model.RunStatusAccepted, source: "stdout", state: "cancelled", cancel: true},
		{name: "nonzero exit", status: model.RunStatusRE, source: "exit_code", state: "failed", exit: 7},
		{name: "missing observation", status: model.RunStatusAccepted, source: "stdout", state: "incomplete", missingReport: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := timeMeasurementRequest()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			report := completeEnvelopeReport()
			if tc.missingReport {
				report = nil
			}
			before := finishWorkMeasurementDiagnostic(ctx, req, model.RunResponse{
				Status: tc.status, ExitCode: &tc.exit, VerdictSource: tc.source,
				CPUTimeMs: 3001, MemoryKB: 32000, WorkMeasurement: report,
			})
			got := finishWorkMeasurementTime(req, before, timeMeasurementConfig())
			d := got.WorkMeasurementExecution
			if d.State != tc.state || d.TimeEstimate != nil || d.NominalTimeExceeded != nil || d.TimeEstimateUnavailable != "execution_"+tc.state {
				t.Fatalf("interrupted execution returned a usable time: %+v", d)
			}
			if got.Status != model.RunStatusInitFail || got.Score != nil || got.CPUTimeNormalization != nil || got.CPUTimeMs != before.CPUTimeMs || got.MemoryKB != before.MemoryKB {
				t.Fatalf("interruption changed diagnostic status or raw measurements: %+v", got)
			}
			if got.WorkMeasurement != nil && !got.WorkMeasurement.ValidUnavailableResult() {
				t.Fatalf("interrupted execution retained totals: %+v", got.WorkMeasurement)
			}
		})
	}
}

func TestWorkMeasurementTimePreservesUnconvertibleCompleteReports(t *testing.T) {
	for name, edit := range map[string]func(*durante.Report){
		"observed signal": func(r *durante.Report) {
			r.SignalsObserved, r.Quality = 1, "signals_observed"
		},
		"partial REP coverage": func(r *durante.Report) {
			unsupported := uint64(1)
			r.UnsupportedREPEntries, r.REPTelemetryStatus = &unsupported, durante.REPPartial
		},
		"nanosecond overflow": func(r *durante.Report) {
			instructions, rep, entries := uint64(math.MaxInt64), uint64(1), uint64(1)
			r.Instructions, r.REPBytes, r.REPEntries = &instructions, &rep, &entries
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := timeMeasurementRequest()
			report := completeEnvelopeReport()
			edit(report)
			before := timeMeasurementDiagnostic(req, model.RunStatusAccepted, report)
			if before.WorkMeasurementExecution.State != "complete" {
				t.Fatal("fixture must have a complete diagnostic observation")
			}
			got := finishWorkMeasurementTime(req, before, timeMeasurementConfig())
			d := got.WorkMeasurementExecution
			if d.State != "complete" || d.TimeEstimate != nil || d.NominalTimeExceeded != nil || d.TimeEstimateUnavailable != "observation_not_convertible" {
				t.Fatalf("unconvertible observation was assigned a time or lost completion: %+v", d)
			}
			if got.WorkMeasurement != report || !got.WorkMeasurement.Complete || got.Status != model.RunStatusInitFail || got.Score != nil {
				t.Fatal("conversion failure discarded the observation or changed the verdict")
			}
		})
	}
}

func TestWorkMeasurementTimeDisabledAndAdmissionFailureRemainUnchanged(t *testing.T) {
	req := envelopeRequest()
	before := timeMeasurementDiagnostic(req, model.RunStatusAccepted, completeEnvelopeReport())
	if got := finishWorkMeasurementTime(req, before, timeMeasurementConfig()); !reflect.DeepEqual(got, before) {
		t.Fatal("unrequested conversion changed the existing diagnostic response")
	}
	req.WorkMeasurementTime = true
	before = model.RunResponse{Status: model.RunStatusInitFail, Reason: "no compatible policy"}
	if got := finishWorkMeasurementTime(req, before, timeMeasurementConfig()); !reflect.DeepEqual(got, before) {
		t.Fatal("pre-execution failure acquired a measurement envelope")
	}
}

func TestWorkMeasurementTimeConstructorsFreezeOperatorPolicy(t *testing.T) {
	measurement := timeMeasurementConfig()
	wantPolicy := *measurement.TimePolicy
	wantResources := *measurement.ResourcePolicy
	cfg := config.Config{
		MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20,
		Execution: config.ExecutionConfig{WorkMeasurement: measurement},
	}
	service := NewWithConfig(cfg)
	remote := newRemoteRunner(cfg).(*remoteRunner)
	measurement.Families[0] = "python"
	measurement.TimePolicy.ModelID = "changed"
	measurement.TimePolicy.InstructionNSNumerator = 100
	measurement.ResourcePolicy.MaxLimits.CPUTimeMs = 4000
	for name, frozen := range map[string]durante.Config{
		"local service": service.workMeasurement, "remote runner": remote.workMeasurement,
	} {
		t.Run(name, func(t *testing.T) {
			policy, runtimeID, err := frozen.TimePolicyFor("CPP20")
			if err != nil || policy != wantPolicy || runtimeID != wantPolicy.RuntimeID || *frozen.ResourcePolicy != wantResources {
				t.Fatalf("constructor retained mutable operator state: policy=%+v, runtime=%q, err=%v", policy, runtimeID, err)
			}
			req := timeMeasurementRequest()
			got := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, completeEnvelopeReport()), frozen)
			if got.WorkMeasurementExecution.TimeEstimate == nil || got.WorkMeasurementExecution.TimeEstimate.Nanoseconds != 1234 {
				t.Fatalf("estimate changed after caller modified its model: %+v", got.WorkMeasurementExecution)
			}
		})
	}
}
