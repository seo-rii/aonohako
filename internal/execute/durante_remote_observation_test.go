package execute

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

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
