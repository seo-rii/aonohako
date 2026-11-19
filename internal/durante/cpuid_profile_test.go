package durante

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeProfiledTree(t *testing.T, dir string, profiles map[int]string) {
	t.Helper()
	for pid, rows := range basicTree() {
		var b strings.Builder
		for _, r := range rows {
			if r.Event == "start" {
				if profile, ok := profiles[pid]; ok && profile != "" {
					version := uint64(1)
					value := profile
					r.CPUIDProfileVersion, r.CPUIDProfile = &version, &value
				}
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			b.Write(raw)
			b.WriteByte('\n')
		}
		name := filepath.Join(dir, "process-10.jsonl")
		if pid == 20 {
			name = filepath.Join(dir, "process-20.jsonl")
		}
		if err := os.WriteFile(name, []byte(b.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseCPUIDProfile(t *testing.T) {
	for _, value := range []string{"", " ", "x86-64-v1", "x86-64-v2"} {
		if _, err := ParseCPUIDProfile(value); err != nil {
			t.Fatalf("%q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"x86-64-v3", "X86-64-V1", "host", "x86-64-v1,x86-64-v2"} {
		if _, err := ParseCPUIDProfile(value); err == nil {
			t.Fatalf("%q accepted", value)
		}
	}
}

func TestCollectorPublishesConsistentCPUIDReceipt(t *testing.T) {
	dir := t.TempDir()
	writeProfiledTree(t, dir, map[int]string{10: CPUIDProfileX8664V2, 20: CPUIDProfileX8664V2})
	if r := CollectDirectory(dir, 10); !r.Complete || r.CPUIDProfile != CPUIDProfileX8664V2 {
		t.Fatalf("profiled tree: %+v", r)
	}
	for name, profiles := range map[string]map[int]string{
		"host-visible child": {10: CPUIDProfileX8664V2},
		"different levels":   {10: CPUIDProfileX8664V2, 20: CPUIDProfileX8664V1},
	} {
		dir := t.TempDir()
		writeProfiledTree(t, dir, profiles)
		if r := CollectDirectory(dir, 10); r.Complete || r.Instructions != nil || r.CPUIDProfile != "" {
			t.Fatalf("%s accepted: %+v", name, r)
		}
	}
}

func TestSessionPassesAndRequiresCPUIDProfile(t *testing.T) {
	for _, tc := range []struct {
		name, configured string
		receipts         map[int]string
		complete         bool
	}{
		{"matching", CPUIDProfileX8664V2, map[int]string{10: CPUIDProfileX8664V2, 20: CPUIDProfileX8664V2}, true},
		{"legacy client", CPUIDProfileX8664V2, nil, false},
		{"other profile", CPUIDProfileX8664V2, map[int]string{10: CPUIDProfileX8664V1, 20: CPUIDProfileX8664V1}, false},
		{"unrequested receipt", "", map[int]string{10: CPUIDProfileX8664V2, 20: CPUIDProfileX8664V2}, false},
		{"unprofiled", "", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			launcher := runtimeInstallationFixture(t, root)
			client := runtimeClientFixture(t, root)
			cfg, err := ParseConfig("cpp", launcher, client, DispatchDefault)
			if err != nil {
				t.Fatal(err)
			}
			cfg.CPUIDProfile = tc.configured
			s, err := Prepare(context.Background(), root, cfg, "CPP20", os.Geteuid(), os.Getegid())
			if err != nil {
				t.Fatal(err)
			}
			wrapped := s.Wrap([]string{"/tmp/target"})
			index := slices.Index(wrapped, "-cpuid_profile")
			if (index >= 0) != (tc.configured != "") || (index >= 0 && wrapped[index+1] != tc.configured) {
				t.Fatalf("launch flag mismatch: %q", wrapped)
			}
			if separator := slices.Index(wrapped, "--"); index >= 0 && index > separator {
				t.Fatal("client option placed after the target separator")
			}
			writeProfiledTree(t, s.dir, tc.receipts)
			r := s.Collect(10)
			if r.Complete != tc.complete {
				t.Fatalf("complete=%v want %v: %+v", r.Complete, tc.complete, r)
			}
			if !tc.complete && (r.Instructions != nil || r.CPUIDProfile != "") {
				t.Fatalf("mismatched receipt exposed totals: %+v", r)
			}
			if tc.complete && r.CPUIDProfile != tc.configured {
				t.Fatalf("receipt %q want %q", r.CPUIDProfile, tc.configured)
			}
		})
	}
}

func TestTimePolicyCPUIDBinding(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	base := `{"model_id":"m","model_version":1,"reference_id":"r","runtime_id":"CPP20@` + image + `","family":"cpp","engine_profile":"` + EngineProfile + `","dispatch_profile":"default","instruction_ns_numerator":1,"rep_byte_ns_numerator":1,"ns_denominator":1`
	policy, err := ParseTimePolicy(base + `,"cpuid_profile":"x86-64-v2"}`)
	if err != nil || policy.CPUIDProfile != CPUIDProfileX8664V2 {
		t.Fatalf("profiled policy: %+v %v", policy, err)
	}
	if legacy, err := ParseTimePolicy(base + `}`); err != nil || legacy.CPUIDProfile != "" {
		t.Fatalf("legacy policy: %+v %v", legacy, err)
	}
	for _, bad := range []string{`,"cpuid_profile":""}`, `,"cpuid_profile":"x86-64-v3"}`, `,"cpuid_profile":null}`} {
		if _, err := ParseTimePolicy(base + bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}

	cfg := judgeConfig()
	cfg.TimePolicy.CPUIDProfile = ""
	cfg.CPUIDProfile = CPUIDProfileX8664V2
	if err := cfg.ValidateTimeConfiguration(); err == nil {
		t.Fatal("host-CPUID policy accepted for a profiled configuration")
	}
	cfg.TimePolicy.CPUIDProfile = CPUIDProfileX8664V2
	if err := cfg.ValidateTimeConfiguration(); err != nil {
		t.Fatal(err)
	}

	instructions, zero := uint64(1000), uint64(0)
	verified := false
	report := Report{Schema: 1, ClientVersion: 1, Complete: true, Quality: "uninterrupted", Authority: Authority, Metric: Metric, Scope: Scope, REPEnabled: true, EngineProfile: EngineProfile, Family: "cpp", DispatchProfile: DispatchDefault, Instructions: &instructions, REPBytes: &zero, REPEntries: &zero, UnsupportedREPEntries: &zero, Processes: 1, Images: 1, IntegrityVerified: &verified, MeasurementContract: DiagnosticContract(), REPTelemetryStatus: REPMovsStosOnly}
	for _, tc := range []struct {
		receipt string
		ok      bool
	}{{CPUIDProfileX8664V2, true}, {"", false}, {CPUIDProfileX8664V1, false}} {
		report.CPUIDProfile = tc.receipt
		estimate, eligibility, err := cfg.TimePolicy.EstimateWithEligibility(cfg.TimePolicy.RuntimeID, report)
		if (err == nil) != tc.ok {
			t.Fatalf("receipt %q: err=%v", tc.receipt, err)
		}
		if tc.ok && (estimate.CPUIDProfile != CPUIDProfileX8664V2 || estimate.Nanoseconds != 1000) {
			t.Fatalf("estimate lacks profile identity: %+v", estimate)
		}
		if !tc.ok && (len(eligibility.ReasonCodes) != 1 || eligibility.ReasonCodes[0] != TimeRuntimeProfileMismatch) {
			t.Fatalf("mismatch reason: %+v", eligibility)
		}
	}
}

func TestTimeModelBindingSelectsCPUIDProfile(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	binding := func(profile string) string {
		field := ""
		if profile != "" {
			field = `,"cpuid_profile":"` + profile + `"`
		}
		return `{"source_language":"CPP20","family":"cpp","runtime_image":"` + image + `","engine_profile":"` + EngineProfile + `","dispatch_profile":"default","model_id":"m","model_version":1,"qualification_id":"q-` + profile + `"` + field + `}`
	}
	raw := `{"schema":1,"models":[{"model_id":"m","model_version":1,"reference_id":"r","coefficients":{"instruction_ns_numerator":1,"rep_byte_ns_numerator":1,"ns_denominator":1}}],"profiles":[` + binding("") + `,` + binding(CPUIDProfileX8664V2) + `]}`
	models, err := ParseTimeModels(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg := judgeConfig()
	cfg.TimePolicy = nil
	cfg.TimeModels = models
	for _, profile := range []string{"", CPUIDProfileX8664V2} {
		cfg.CPUIDProfile = profile
		policy, _, err := cfg.TimePolicyFor("CPP20")
		if err != nil || policy.CPUIDProfile != profile {
			t.Fatalf("profile %q selected %+v (%v)", profile, policy, err)
		}
	}
	cfg.CPUIDProfile = CPUIDProfileX8664V1
	if _, _, err := cfg.TimePolicyFor("CPP20"); err == nil {
		t.Fatal("selected a binding qualified under another CPUID profile")
	}
	duplicate := `{"schema":1,"models":[{"model_id":"m","model_version":1,"reference_id":"r","coefficients":{"instruction_ns_numerator":1,"rep_byte_ns_numerator":1,"ns_denominator":1}}],"profiles":[` + binding(CPUIDProfileX8664V2) + `,` + binding(CPUIDProfileX8664V2) + `]}`
	if _, err := ParseTimeModels(duplicate); err == nil {
		t.Fatal("accepted duplicate binding for one CPUID profile")
	}
	invalid := strings.Replace(raw, `"cpuid_profile":"x86-64-v2"`, `"cpuid_profile":""`, 1)
	if _, err := ParseTimeModels(invalid); err == nil {
		t.Fatal("accepted an empty explicit CPUID profile binding")
	}
}
