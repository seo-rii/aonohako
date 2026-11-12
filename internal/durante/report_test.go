package durante

import (
	"encoding/json"
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
	for _, field := range []string{"integrity_verified", "trusted_verdict_eligible"} {
		old := `"` + field + `":false`
		mutated := strings.Replace(string(data), old, `"`+field+`":true,`+old, 1)
		var decoded Report
		if err := json.Unmarshal([]byte(mutated), &decoded); err == nil {
			t.Fatal("duplicate contract claim accepted", field)
		}
	}
}
