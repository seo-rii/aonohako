package execute

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestTimeEligibilityLocalAndRemotePreserveReasonsAndRawReports(t *testing.T) {
	for _, tc := range []struct {
		name string
		v2   bool
		edit func(*durante.Report)
		want []string
	}{
		{"v1 legacy detail", false, func(*durante.Report) {}, []string{}},
		{"v2 missing detail", true, func(*durante.Report) {}, []string{durante.TimeREPBreakdownMissing}},
		{"v2 observed zero", true, func(r *durante.Report) { r.REPBreakdown = &durante.REPBreakdown{Version: 1} }, []string{}},
		{"signals", false, func(r *durante.Report) { r.SignalsObserved, r.Quality = 1, "signals_observed" }, []string{durante.TimeSignalsObserved}},
		{"partial REP", false, func(r *durante.Report) {
			count := uint64(1)
			r.UnsupportedREPEntries, r.REPTelemetryStatus = &count, durante.REPPartial
		}, []string{durante.TimeUnsupportedREP}},
		{"signals partial missing", true, func(r *durante.Report) {
			r.SignalsObserved, r.Quality = 1, "signals_observed"
			count := uint64(1)
			r.UnsupportedREPEntries, r.REPTelemetryStatus = &count, durante.REPPartial
		}, []string{durante.TimeSignalsObserved, durante.TimeUnsupportedREP, durante.TimeREPBreakdownMissing}},
		{"overflow", false, func(r *durante.Report) { count := uint64(math.MaxUint64); r.Instructions = &count }, []string{durante.TimeNumericOverflow}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, cfg := timeMeasurementRequest(), timeMeasurementConfig()
			if tc.v2 {
				cfg = repTimeMeasurementConfig()
			}
			report := completeEnvelopeReport()
			tc.edit(report)
			before := timeMeasurementDiagnostic(req, model.RunStatusAccepted, report)
			local := finishWorkMeasurementTime(req, before, cfg)
			d := local.WorkMeasurementExecution
			if d.TimeEstimateEligibility == nil || d.TimeEstimateEligibility.Validate() != nil || !reflect.DeepEqual(d.TimeEstimateEligibility.ReasonCodes, tc.want) || d.TimeEstimateEligibility.Eligible != (len(tc.want) == 0) {
				t.Fatalf("incorrect conversion reasons: %+v", d.TimeEstimateEligibility)
			}
			if len(tc.want) != 0 && (d.TimeEstimateUnavailable != "observation_not_convertible" || d.TimeEstimate != nil || d.NominalTimeExceeded != nil) {
				t.Fatalf("new detail changed legacy availability: %+v", d)
			}
			if len(tc.want) == 0 && (d.TimeEstimateUnavailable != "" || d.TimeEstimate == nil) {
				t.Fatal("eligible report lost its estimate")
			}
			if local.WorkMeasurement != report || !report.Complete || local.CPUTimeNs != before.CPUTimeNs || local.Status != model.RunStatusInitFail || local.Score != nil || before.WorkMeasurementExecution.TimeEstimateEligibility != nil {
				t.Fatal("assessment changed raw observation, verdict, or input envelope")
			}
			remote := exchangeTimeMeasurementResult(t, req, local, cfg)
			if !reflect.DeepEqual(remote, local) {
				t.Fatalf("SSE changed eligibility: local=%+v remote=%+v", local, remote)
			}
		})
	}
}

func TestTimeEligibilityExecutionAndConfigurationHaveDocumentedPriority(t *testing.T) {
	for _, tc := range []struct {
		state, status string
		cancel        bool
	}{
		{"resource_exhausted", model.RunStatusTLE, false},
		{"failed", model.RunStatusRE, false},
		{"cancelled", model.RunStatusTLE, true},
		{"incomplete", model.RunStatusAccepted, false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			req, cfg := timeMeasurementRequest(), timeMeasurementConfig()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			before := finishWorkMeasurementDiagnostic(ctx, req, model.RunResponse{Status: tc.status, WorkMeasurement: completeEnvelopeReport()})
			local := finishWorkMeasurementTime(req, before, cfg)
			d := local.WorkMeasurementExecution
			want := "execution_" + tc.state
			if d.State != tc.state || d.TimeEstimateUnavailable != want || d.TimeEstimateEligibility == nil || !reflect.DeepEqual(d.TimeEstimateEligibility.ReasonCodes, []string{want}) || d.TimeEstimate != nil || !local.WorkMeasurement.ValidUnavailableResult() {
				t.Fatalf("interruption was promoted or masked: %+v", d)
			}
			if got := exchangeTimeMeasurementResult(t, req, local, cfg); !reflect.DeepEqual(got, local) {
				t.Fatal("SSE changed interrupted eligibility", got)
			}
		})
	}
	for _, invalidReport := range []bool{false, true} {
		req, cfg := timeMeasurementRequest(), timeMeasurementConfig()
		local := timeMeasurementDiagnostic(req, model.RunStatusAccepted, completeEnvelopeReport())
		cfg.TimePolicy = nil
		want := durante.TimeIncompatibleConfiguration
		if invalidReport {
			local.WorkMeasurement = nil
			want = durante.TimeIncompatibleObservation
		}
		d := finishWorkMeasurementTime(req, local, cfg).WorkMeasurementExecution
		if !reflect.DeepEqual(d.TimeEstimateEligibility.ReasonCodes, []string{want}) || d.TimeEstimateUnavailable != want {
			t.Fatal("stage priority changed", d)
		}
	}
}

func TestRemoteTimeEligibilityRejectsChangedClaimsAndPreservesLegacyAbsence(t *testing.T) {
	for _, missingLegacyField := range []bool{false, true} {
		req, cfg := timeMeasurementRequest(), timeMeasurementConfig()
		report := completeEnvelopeReport()
		report.SignalsObserved, report.Quality = 1, "signals_observed"
		local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, report), cfg)
		if missingLegacyField {
			local.WorkMeasurementExecution.TimeEstimateEligibility = nil
		}
		if got := exchangeTimeMeasurementResult(t, req, local, cfg); !reflect.DeepEqual(got, local) {
			t.Fatal("legacy absence changed old unavailable validation", got)
		}
		local.WorkMeasurementExecution.TimeEstimateUnavailable = "execution_cancelled"
		if got := exchangeTimeMeasurementResult(t, req, local, cfg); got.WorkMeasurementExecution != nil || !strings.Contains(got.Reason, "invalid remote result") {
			t.Fatal("legacy absence bypassed exact old validation", got)
		}
	}
	for name, replacement := range map[string]durante.TimeEligibility{
		"promoted":                    {Version: 1, Eligible: true, ReasonCodes: []string{}},
		"other reason":                {Version: 1, ReasonCodes: []string{durante.TimeUnsupportedREP}},
		"missing simultaneous reason": {Version: 1, ReasonCodes: []string{durante.TimeSignalsObserved}},
	} {
		t.Run(name, func(t *testing.T) {
			req, cfg := timeMeasurementRequest(), timeMeasurementConfig()
			report := completeEnvelopeReport()
			report.SignalsObserved, report.Quality = 1, "signals_observed"
			unsupported := uint64(1)
			report.UnsupportedREPEntries, report.REPTelemetryStatus = &unsupported, durante.REPPartial
			local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, report), cfg)
			local.WorkMeasurementExecution.TimeEstimateEligibility = &replacement
			if got := exchangeTimeMeasurementResult(t, req, local, cfg); got.WorkMeasurementExecution != nil || !strings.Contains(got.Reason, "eligibility mismatch") {
				t.Fatal("changed eligibility survived SSE", got)
			}
		})
	}
}

func TestTimeEligibilityIsNotPublishedOrAcceptedWithoutConversionRequest(t *testing.T) {
	req, cfg := envelopeRequest(), timeMeasurementConfig()
	local := timeMeasurementDiagnostic(req, model.RunStatusAccepted, completeEnvelopeReport())
	got := finishWorkMeasurementTime(req, local, cfg)
	if got.WorkMeasurementExecution.TimeEstimateEligibility != nil || !reflect.DeepEqual(got, local) {
		t.Fatal("unrequested eligibility was emitted")
	}
	local.WorkMeasurementExecution.TimeEstimateEligibility = &durante.TimeEligibility{Version: 1, Eligible: true, ReasonCodes: []string{}}
	got = exchangeTimeMeasurementResult(t, req, local, cfg)
	if got.WorkMeasurementExecution != nil || !strings.Contains(got.Reason, "unsolicited work measurement time") {
		t.Fatal("unrequested eligibility survived SSE", got)
	}
}
