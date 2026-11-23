package execute

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestCompletedSandboxMeasurementSurvivesSlowCollection(t *testing.T) {
	for _, exit := range []int{0, 7} {
		t.Run(fmt.Sprintf("exit_%d", exit), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const budget = 100 * time.Millisecond
				ctx, release, stop := sandboxExecutionContext(context.Background(), time.Second, budget)
				defer stop()
				if !release() {
					t.Fatal("could not release target execution budget")
				}
				// The target finishes just before its deadline. Virtual time
				// makes collection cross that deadline deterministically.
				time.Sleep(budget - time.Millisecond)
				result := execResult{Status: "OK", ExitCode: &exit, targetPID: 42, CPUTimeNs: 123}
				stoppedBeforeCollection := false
				result = collectCompletedSandboxWorkMeasurement(ctx, stop, result, func(pid int) *durante.Report {
					if pid != 42 {
						t.Fatalf("collector root pid = %d, want 42", pid)
					}
					stoppedBeforeCollection = context.Cause(ctx) == context.Canceled
					time.Sleep(2 * budget)
					return completeEnvelopeReport()
				})
				if !stoppedBeforeCollection {
					t.Fatal("target execution budget remained active during collection")
				}
				if result.Status != "OK" || result.ExitCode == nil || *result.ExitCode != exit || result.CPUTimeNs != 123 || !validDiagnosticWorkReport("CPP20", result.WorkMeasurement) {
					t.Fatalf("slow collection changed completed execution or discarded totals: %+v; report=%+v", result, result.WorkMeasurement)
				}
				if exit == 0 {
					response := finishWorkMeasurementDiagnostic(context.Background(), envelopeRequest(), model.RunResponse{
						Status: model.RunStatusAccepted, ExitCode: &exit, VerdictSource: "stdout", WorkMeasurement: result.WorkMeasurement,
					})
					if response.WorkMeasurementExecution.State != "complete" || response.WorkMeasurementExecution.OutputMatches == nil || !*response.WorkMeasurementExecution.OutputMatches {
						t.Fatalf("slow collection made the completed envelope unavailable: %+v", response.WorkMeasurementExecution)
					}
				}
			})
		})
	}
}

func TestCompletedSandboxMeasurementRetainsExecutionFailures(t *testing.T) {
	for _, tc := range []struct {
		name, status, source string
		cancel, deadline     bool
	}{
		{name: "parent cancellation", status: "OK", cancel: true},
		{name: "execution deadline", status: "OK", deadline: true},
		{name: "CPU failure", status: model.RunStatusTLE, source: "cpu_time_final"},
		{name: "memory failure", status: model.RunStatusMLE, source: "memory_cgroup_final"},
		{name: "stream failure", status: model.RunStatusRE, source: "stream_io"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const budget = 100 * time.Millisecond
				parent, cancelParent := context.WithCancel(context.Background())
				defer cancelParent()
				ctx, release, stop := sandboxExecutionContext(parent, time.Second, budget)
				defer stop()
				if !release() {
					t.Fatal("could not release target execution budget")
				}
				if tc.cancel {
					cancelParent()
				}
				if tc.deadline {
					time.Sleep(budget + time.Millisecond)
				}
				zero := 0
				result := execResult{Status: tc.status, VerdictSource: tc.source, ExitCode: &zero, CPUTimeNs: 123}
				result = collectCompletedSandboxWorkMeasurement(ctx, stop, result, func(int) *durante.Report {
					time.Sleep(2 * budget)
					return completeEnvelopeReport()
				})
				if result.Status != tc.status || result.VerdictSource != tc.source || result.ExitCode == nil || *result.ExitCode != 0 || result.CPUTimeNs != 123 {
					t.Fatalf("collection changed the original execution result: %+v", result)
				}
				if !result.WorkMeasurement.ValidUnavailableResult() || !result.WorkMeasurement.ValidDiagnosticContract() {
					t.Fatalf("failed execution retained completed totals: %+v", result.WorkMeasurement)
				}
			})
		})
	}
}

func TestCompletedSandboxMeasurementEnvelopePreservesParentCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancelParent := context.WithCancel(context.Background())
		defer cancelParent()
		ctx, release, stop := sandboxExecutionContext(parent, time.Second, time.Second)
		defer stop()
		if !release() {
			t.Fatal("could not release target execution budget")
		}
		zero := 0
		result := collectCompletedSandboxWorkMeasurement(ctx, stop, execResult{Status: "OK", ExitCode: &zero}, func(int) *durante.Report {
			cancelParent()
			time.Sleep(2 * time.Second)
			return completeEnvelopeReport()
		})
		if !validDiagnosticWorkReport("CPP20", result.WorkMeasurement) {
			t.Fatal("completed target lost its observation before envelope finalization")
		}
		response := finishWorkMeasurementDiagnostic(parent, envelopeRequest(), model.RunResponse{
			Status: model.RunStatusAccepted, ExitCode: &zero, VerdictSource: "stdout", WorkMeasurement: result.WorkMeasurement,
		})
		if response.WorkMeasurementExecution.State != "cancelled" || response.WorkMeasurementExecution.TerminationSource != "request_context" || response.Score != nil || !response.WorkMeasurement.ValidUnavailableResult() {
			t.Fatalf("execution timer cleanup detached envelope parent cancellation: %+v; report=%+v", response.WorkMeasurementExecution, response.WorkMeasurement)
		}
	})
}
