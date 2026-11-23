package execute

import (
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func repTimeMeasurementConfig() durante.Config {
	cfg := timeMeasurementConfig()
	p := cfg.TimePolicy
	p.Method, p.ModelVersion = durante.TimeMethodREPBreakdown, 2
	p.InstructionNSNumerator, p.REPByteNSNumerator = 2, 0
	p.MOVSByteNSNumerator, p.STOSByteNSNumerator, p.NSDenominator = 3, 5, 4
	return cfg
}

func repTimeMeasurementReport() *durante.Report {
	r := completeEnvelopeReport()
	bytes, entries := uint64(131), uint64(6)
	r.REPBytes, r.REPEntries = &bytes, &entries
	r.REPBreakdown = &durante.REPBreakdown{Version: 1, MOVSBytes: 65, STOSBytes: 66, MOVSEntries: 3, STOSEntries: 3}
	return r
}

func TestREPOperationTimeLocalAndRemotePreserveFormula(t *testing.T) {
	for _, status := range []string{model.RunStatusAccepted, model.RunStatusWA} {
		t.Run(status, func(t *testing.T) {
			req, cfg := timeMeasurementRequest(), repTimeMeasurementConfig()
			before := timeMeasurementDiagnostic(req, status, repTimeMeasurementReport())
			local := finishWorkMeasurementTime(req, before, cfg)
			estimate := local.WorkMeasurementExecution.TimeEstimate
			if estimate == nil || estimate.Method != durante.TimeMethodREPBreakdown || estimate.Nanoseconds != 749 || estimate.MOVSByteNSNumerator != 3 || estimate.STOSByteNSNumerator != 5 || estimate.REPByteNSNumerator != 0 {
				t.Fatalf("operation costs changed: %+v", estimate)
			}
			if local.Status != model.RunStatusInitFail || local.Score != nil || local.CPUTimeMs != before.CPUTimeMs || local.WorkMeasurementExecution.OutputMatches == nil || *local.WorkMeasurementExecution.OutputMatches != (status == model.RunStatusAccepted) {
				t.Fatalf("conversion changed the diagnostic execution: %+v", local)
			}
			if got := exchangeTimeMeasurementResult(t, req, local, cfg); !reflect.DeepEqual(got, local) {
				t.Fatalf("SSE changed v2 conversion: local=%+v remote=%+v", local, got)
			}
		})
	}
}

func TestREPOperationTimeRemoteRecomputesFromLocalModel(t *testing.T) {
	for name, edit := range map[string]func(*model.RunResponse){
		"method":        func(r *model.RunResponse) { r.WorkMeasurementExecution.TimeEstimate.Method = durante.TimeMethod },
		"MOVS weight":   func(r *model.RunResponse) { r.WorkMeasurementExecution.TimeEstimate.MOVSByteNSNumerator++ },
		"STOS weight":   func(r *model.RunResponse) { r.WorkMeasurementExecution.TimeEstimate.STOSByteNSNumerator++ },
		"combined cost": func(r *model.RunResponse) { r.WorkMeasurementExecution.TimeEstimate.REPByteNSNumerator = 1 },
		"same aggregate different partition": func(r *model.RunResponse) {
			r.WorkMeasurement.REPBreakdown.MOVSBytes++
			r.WorkMeasurement.REPBreakdown.STOSBytes--
		},
		"legacy report": func(r *model.RunResponse) { r.WorkMeasurement.REPBreakdown = nil },
	} {
		t.Run(name, func(t *testing.T) {
			req, cfg := timeMeasurementRequest(), repTimeMeasurementConfig()
			local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, repTimeMeasurementReport()), cfg)
			edit(&local)
			got := exchangeTimeMeasurementResult(t, req, local, cfg)
			if got.WorkMeasurementExecution != nil || !strings.Contains(got.Reason, "invalid remote result") {
				t.Fatalf("changed v2 conversion accepted: %+v", got)
			}
		})
	}
}

func TestREPOperationTimeMissingDetailRemainsUnavailable(t *testing.T) {
	req, cfg := timeMeasurementRequest(), repTimeMeasurementConfig()
	report := completeEnvelopeReport()
	local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, report), cfg)
	d := local.WorkMeasurementExecution
	if d.State != "complete" || d.TimeEstimate != nil || d.NominalTimeExceeded != nil || d.TimeEstimateUnavailable != "observation_not_convertible" || local.WorkMeasurement != report {
		t.Fatalf("missing detail became observed zero or discarded raw report: %+v", local)
	}
	if got := exchangeTimeMeasurementResult(t, req, local, cfg); !reflect.DeepEqual(got, local) {
		t.Fatalf("remote changed unavailable detail: %+v", got)
	}
}
