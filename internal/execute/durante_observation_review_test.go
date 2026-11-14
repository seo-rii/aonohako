package execute

import (
	"context"
	"testing"

	"aonohako/internal/model"
)

func TestIncompleteSandboxExecutionDiscardsForgedCompleteObservation(t *testing.T) {
	for _, tc := range []struct {
		name, status, source string
		exit                 int
		cancel, complete     bool
	}{
		{"normal", "OK", "", 0, false, true},
		{"normal nonzero", "OK", "", 7, false, true},
		{"process limit", model.RunStatusRE, "pids_cgroup", 0, false, false},
		{"final process limit", model.RunStatusRE, "pids_cgroup_final", 0, false, false},
		{"stream error", model.RunStatusRE, "stream_io", 0, false, false},
		{"wait error", model.RunStatusRE, "wait_status", 0, false, false},
		{"cancellation", "OK", "", 0, true, false},
		{"signal", "OK", "", -1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			result := execResult{Status: tc.status, VerdictSource: tc.source, ExitCode: &tc.exit,
				CPUTimeNs: 100, WorkMeasurement: completeEnvelopeReport()}
			discardIncompleteWorkMeasurement(ctx.Err() != nil, &result)
			if result.Status != tc.status || result.VerdictSource != tc.source || *result.ExitCode != tc.exit || result.CPUTimeNs != 100 {
				t.Fatal("observation finalization changed execution verdict or resources")
			}
			if result.WorkMeasurement.Complete != tc.complete || !result.WorkMeasurement.ValidDiagnosticContract() {
				t.Fatalf("unexpected diagnostic state: %+v", result.WorkMeasurement)
			}
			if !tc.complete && !result.WorkMeasurement.ValidUnavailableResult() {
				t.Fatal("incomplete execution retained totals")
			}
			if tc.complete && (result.WorkMeasurement.Instructions == nil || *result.WorkMeasurement.Instructions != 1234) {
				t.Fatal("normal execution lost diagnostic totals")
			}
		})
	}
}

func TestRemoteInfrastructureFailureCannotRetainDiagnosticTotals(t *testing.T) {
	req := &model.RunRequest{Lang: "CPP20", WorkMeasurement: true}
	zero := 0
	for _, source := range []string{"pids_cgroup", "pids_cgroup_final", "stream_io", "wait_status"} {
		t.Run(source, func(t *testing.T) {
			result := model.RunResponse{Status: model.RunStatusRE, VerdictSource: source, ExitCode: &zero, WorkMeasurement: completeEnvelopeReport()}
			if validateRemoteWorkMeasurement(req, result) == nil {
				t.Fatal("remote infrastructure failure retained complete totals")
			}
			result.WorkMeasurement = result.WorkMeasurement.WithoutTotals("execution failed")
			if err := validateRemoteWorkMeasurement(req, result); err != nil {
				t.Fatal("unavailable observation rejected", err)
			}
		})
	}
	nonzero := 7
	normal := model.RunResponse{Status: model.RunStatusRE, VerdictSource: "exit_code", ExitCode: &nonzero, WorkMeasurement: completeEnvelopeReport()}
	if err := validateRemoteWorkMeasurement(req, normal); err != nil {
		t.Fatal("normal nonzero execution lost complete diagnostic counts", err)
	}
}
