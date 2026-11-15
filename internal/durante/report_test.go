package durante

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteDiagnosticContractRequiresExplicitUnpromotedFields(t *testing.T) {
	report, err := aggregate(basicTree(), 10)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing integrity":        func(r map[string]any) { delete(r, "integrity_verified") },
		"null integrity":           func(r map[string]any) { r["integrity_verified"] = nil },
		"promoted integrity":       func(r map[string]any) { r["integrity_verified"] = true },
		"claimed target libc":      func(r map[string]any) { r["target_libc_attested"] = true },
		"missing contract":         func(r map[string]any) { delete(r, "measurement_contract") },
		"unknown version":          func(r map[string]any) { r["measurement_contract"].(map[string]any)["version"] = 2 },
		"claimed external backend": func(r map[string]any) { r["measurement_contract"].(map[string]any)["backend"] = "perf" },
		"missing eligibility":      func(r map[string]any) { delete(r["measurement_contract"].(map[string]any), "trusted_verdict_eligible") },
		"null eligibility":         func(r map[string]any) { r["measurement_contract"].(map[string]any)["trusted_verdict_eligible"] = nil },
		"promoted eligibility":     func(r map[string]any) { r["measurement_contract"].(map[string]any)["trusted_verdict_eligible"] = true },
		"missing conversion":       func(r map[string]any) { delete(r["measurement_contract"].(map[string]any), "cpu_time_conversion") },
		"claimed conversion":       func(r map[string]any) { r["measurement_contract"].(map[string]any)["cpu_time_conversion"] = 1 },
		"retired semantics": func(r map[string]any) {
			r["measurement_contract"].(map[string]any)["instruction_semantics"] = "retired"
		},
		"lane weighting": func(r map[string]any) { r["measurement_contract"].(map[string]any)["simd_weighting"] = "active_lanes" },
		"case alias":     func(r map[string]any) { r["INTEGRITY_VERIFIED"] = false; r["integrity_verified"] = true },
		"contract alias": func(r map[string]any) { r["measurement_contract"].(map[string]any)["VERSION"] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			mutate(value)
			changed, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Report
			if err := json.Unmarshal(changed, &decoded); err == nil && decoded.ValidDiagnosticContract() {
				t.Fatal("accepted absent or promoted contract")
			}
		})
	}
	var roundTrip Report
	if err := json.Unmarshal(data, &roundTrip); err != nil || !roundTrip.ValidDiagnosticContract() {
		t.Fatal("valid contract did not survive JSON", err)
	}
}

func TestCollectorOwnsTrustAndREPCoverageLabels(t *testing.T) {
	for _, tc := range []struct {
		enabled     bool
		unsupported uint64
		status      string
	}{{false, 0, REPDisabled}, {true, 0, REPMovsStosOnly}, {true, 7, REPPartial}} {
		dir := t.TempDir()
		s := start(10, "10-1", "initial", 0, "")
		s.REPEnabled = tc.enabled
		e := end(s, 2, 123)
		e.UnsupportedREPEntries = tc.unsupported
		var lines []string
		for _, row := range []event{s, e} {
			data, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, strings.TrimSuffix(string(data), "}")+`,"integrity_verified":true,"trusted_verdict_eligible":true,"rep_telemetry_status":"complete"}`)
		}
		file := filepath.Join(dir, "process-10.jsonl")
		if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		r := CollectDirectory(dir, 10)
		if !r.Complete || !r.ValidDiagnosticContract() || !r.ValidREPTelemetry() || r.REPTelemetryStatus != tc.status || *r.Instructions != 123 {
			t.Fatalf("wrong observation: %+v", r)
		}
		if err := os.WriteFile(file, []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		r = CollectDirectory(dir, 10)
		if r.Complete || !r.ValidDiagnosticContract() || r.REPTelemetryStatus != REPUnavailable || r.Instructions != nil || r.REPBytes != nil {
			t.Fatalf("failure promoted a count: %+v", r)
		}
	}
	s := start(10, "10-1", "initial", 0, "")
	e := end(s, 2, 123)
	e.REPBytes = 1
	if _, err := aggregate(map[int][]event{10: {s, e}}, 10); err == nil {
		t.Fatal("accepted bytes without a REP entry")
	}
}

func TestRemoteDiagnosticContractRejectsDuplicateClaims(t *testing.T) {
	r, err := aggregate(basicTree(), 10)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"integrity_verified", "trusted_verdict_eligible", "target_libc_attested"} {
		old := `"` + field + `":false`
		mutated := strings.Replace(string(data), old, `"`+field+`":true,`+old, 1)
		var decoded Report
		if err := json.Unmarshal([]byte(mutated), &decoded); err == nil {
			t.Fatal("duplicate contract claim accepted", field)
		}
	}
}

func TestUnavailableObservationCannotAttestTargetLibc(t *testing.T) {
	r := incomplete("forced stop")
	r.TargetLibcAttested = true
	if !r.ValidUnavailableResult() || r.ValidDiagnosticContract() {
		t.Fatal("unavailable observation accepted unsupported libc attestation")
	}
	r.TargetLibcAttested = false
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(data), `"target_libc_attested":false`, `"TARGET_LIBC_ATTESTED":true,"target_libc_attested":false`, 1)
	var decoded Report
	if err := json.Unmarshal([]byte(mutated), &decoded); err == nil {
		t.Fatal("case-alias attestation contradiction accepted")
	}
}

func detailedReportFixture(t *testing.T) Report {
	t.Helper()
	s := advertiseREP(start(10, "10-1", "initial", 0, ""))
	part := REPBreakdown{Version: 1, MOVSBytes: 1<<53 + 1, STOSBytes: 7, MOVSEntries: 2, STOSEntries: 3}
	r, err := aggregate(map[int][]event{10: {s, withREPBreakdown(end(s, 2, 123), part)}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReportREPBreakdownJSONPreservesExactDetailAndRejectsMalformedShapes(t *testing.T) {
	r := detailedReportFixture(t)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Report
	if err := json.Unmarshal(raw, &decoded); err != nil || !decoded.ValidREPTelemetry() || decoded.REPBreakdown == nil || *decoded.REPBreakdown != *r.REPBreakdown {
		t.Fatalf("exact detailed report did not survive remote JSON: %+v, %v", decoded, err)
	}
	part, err := json.Marshal(r.REPBreakdown)
	if err != nil {
		t.Fatal(err)
	}
	replace := func(object string) string { return strings.Replace(string(raw), string(part), object, 1) }
	reject := func(name, wire string) {
		t.Helper()
		var bad Report
		if err := json.Unmarshal([]byte(wire), &bad); err == nil {
			t.Fatalf("accepted malformed remote REP detail %s: %s", name, wire)
		}
	}
	for _, object := range []string{"null", "true", "[]", "[1,0,0,0,0]", "{}", `"legacy"`} {
		reject(object, replace(object))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(part, &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		for _, value := range []string{"null", "true", "1.0", "1e0", "-1", `"1"`, "18446744073709551616", "{}", "[]"} {
			changed := make(map[string]json.RawMessage, len(fields))
			for name, original := range fields {
				changed[name] = original
			}
			changed[key] = json.RawMessage(value)
			encoded, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			reject(key+"="+value, replace(string(encoded)))
		}
		changed := make(map[string]json.RawMessage, len(fields))
		for name, original := range fields {
			if name != key {
				changed[name] = original
			}
		}
		encoded, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		reject("missing "+key, replace(string(encoded)))
		changed[strings.ToUpper(key)] = fields[key]
		encoded, err = json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		reject("case alias "+key, replace(string(encoded)))
		old := `"` + key + `":` + string(fields[key])
		reject("duplicate "+key, replace(strings.Replace(string(part), old, old+","+old, 1)))
	}
	for _, version := range []string{"0", "2", "18446744073709551615"} {
		reject("unsupported version", replace(strings.Replace(string(part), `"version":1`, `"version":`+version, 1)))
	}
	reject("unknown member", replace(strings.TrimSuffix(string(part), "}")+`,"extra":0}`))
	reject("top-level case alias", strings.Replace(string(raw), `"rep_breakdown":`, `"REP_BREAKDOWN":`, 1))
	member := `"rep_breakdown":` + string(part)
	reject("duplicate top-level field", strings.Replace(string(raw), member, member+","+member, 1))
	reject("escaped duplicate", replace(strings.Replace(string(part), `"version":1`, `"ver\u0073ion":1,"version":1`, 1)))
}

func TestReportREPTelemetryValidatesOptionalDetailPartition(t *testing.T) {
	for name, edit := range map[string]func(*Report){
		"unknown version":          func(r *Report) { r.REPBreakdown.Version = 2 },
		"byte partition":           func(r *Report) { r.REPBreakdown.MOVSBytes++ },
		"entry partition":          func(r *Report) { r.REPBreakdown.STOSEntries++ },
		"MOVS bytes without entry": func(r *Report) { r.REPBreakdown.MOVSEntries = 0; r.REPBreakdown.STOSEntries = *r.REPEntries },
		"STOS bytes without entry": func(r *Report) { r.REPBreakdown.STOSEntries = 0; r.REPBreakdown.MOVSEntries = *r.REPEntries },
		"byte overflow": func(r *Report) {
			r.REPBreakdown.MOVSBytes, r.REPBreakdown.STOSBytes, r.REPBytes = math.MaxUint64, 1, repCount(0)
		},
		"entry overflow": func(r *Report) {
			r.REPBreakdown.MOVSEntries, r.REPBreakdown.STOSEntries, r.REPEntries = math.MaxUint64, 1, repCount(0)
		},
		"disabled detail": func(r *Report) {
			r.REPEnabled, r.REPTelemetryStatus = false, REPDisabled
			r.REPBytes, r.REPEntries = repCount(0), repCount(0)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := detailedReportFixture(t)
			edit(&r)
			if r.ValidREPTelemetry() {
				t.Fatalf("contradictory detail passed existing telemetry gate: %+v", r.REPBreakdown)
			}
		})
	}
	r := detailedReportFixture(t)
	r.REPBreakdown = nil
	if !r.ValidREPTelemetry() {
		t.Fatal("legacy report now requires detail")
	}
	r = detailedReportFixture(t)
	r.REPEnabled, r.REPTelemetryStatus = false, REPDisabled
	r.REPBytes, r.REPEntries = repCount(0), repCount(0)
	r.REPBreakdown = &REPBreakdown{Version: 1}
	if !r.ValidREPTelemetry() {
		t.Fatal("observed zero detail is not valid with REP disabled")
	}
}

func TestUnavailableReportCannotRetainREPBreakdown(t *testing.T) {
	r := detailedReportFixture(t)
	want := *r.REPBreakdown
	stopped := r.WithoutTotals("request cancelled")
	if stopped.REPBreakdown != nil || !stopped.ValidUnavailableResult() || r.REPBreakdown == nil || *r.REPBreakdown != want {
		t.Fatalf("execution stop retained detail or changed the original: stopped=%+v original=%+v", stopped, r)
	}
	raw, err := json.Marshal(stopped)
	if err != nil || strings.Contains(string(raw), "rep_breakdown") {
		t.Fatalf("unavailable JSON exposed detail: %s, %v", raw, err)
	}
	stopped.REPBreakdown = &REPBreakdown{Version: 1}
	if stopped.ValidUnavailableResult() || stopped.ValidREPTelemetry() {
		t.Fatal("unavailable observation claims measured zero detail")
	}
}
