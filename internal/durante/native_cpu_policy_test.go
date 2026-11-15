package durante

import (
	"aonohako/internal/cpupolicy"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func cpuText(value string) *string { return &value }

func cpuStart(base event, policy string) event {
	base.CPUPolicyVersion = repCount(1)
	base.CPUPolicy = cpuText(policy)
	return base
}

func TestNativeCPUPolicyPreservesNativeCountsAndNormalExit126(t *testing.T) {
	for _, policy := range []string{"avx", "simd", "avx,simd"} {
		t.Run(policy, func(t *testing.T) {
			base := start(10, "10-1", "initial", 0, "")
			last := end(base, 2, 100)
			last.ExitCode = 126
			r := collectREPRows(t, map[int][]event{10: {cpuStart(base, policy), last}})
			var want []cpupolicy.Feature
			for _, value := range strings.Split(policy, ",") {
				want = append(want, cpupolicy.Feature(value))
			}
			if !r.Complete || r.Instructions == nil || *r.Instructions != 100 || !reflect.DeepEqual(r.DisallowedCPUFeatures, want) || !r.ValidDiagnosticContract() {
				t.Fatalf("native policy report: %+v", r)
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Report
			if err := json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded.DisallowedCPUFeatures, want) {
				t.Fatalf("policy lost across report serialization: %s, %v", raw, err)
			}
		})
	}
	legacy := collectREPRows(t, basicTree())
	raw, err := json.Marshal(legacy)
	if err != nil || legacy.DisallowedCPUFeatures != nil || strings.Contains(string(raw), "disallowed_cpu_features") {
		t.Fatalf("legacy report acquired a policy: %s, %v", raw, err)
	}
}

func TestNativeCPUPolicyValidatesAllProcessAndExecStarts(t *testing.T) {
	rows := basicTree()
	child := rows[20][0]
	checkpoint := end(child, 2, 200)
	checkpoint.Event, checkpoint.ExitSeen = "exec_attempt", false
	image := start(20, "20-2", "initial", 0, "")
	rows[20] = []event{child, checkpoint, image, end(image, 2, 300)}
	for pid := range rows {
		for i := range rows[pid] {
			if rows[pid][i].Event == "start" {
				rows[pid][i] = cpuStart(rows[pid][i], "avx,simd")
			}
		}
	}
	if r := collectREPRows(t, rows); !r.Complete || *r.Instructions != 600 || len(r.DisallowedCPUFeatures) != 2 {
		t.Fatalf("matching fork/exec policy rejected: %+v", r)
	}
	for _, where := range [][2]int{{10, 0}, {20, 0}, {20, 2}} {
		for _, other := range []string{"avx", "simd", ""} {
			saved := rows[where[0]][where[1]]
			changed := saved
			if other == "" {
				changed.CPUPolicyVersion, changed.CPUPolicy = nil, nil
			} else {
				changed.CPUPolicy = cpuText(other)
			}
			rows[where[0]][where[1]] = changed
			if r := collectREPRows(t, rows); !r.ValidUnavailableResult() || r.Reason != "mixed CPU policies" {
				t.Fatalf("mixed policy accepted: %+v", r)
			}
			rows[where[0]][where[1]] = saved
		}
	}
}

func TestNativeCPUPolicyViolationRequiresLiveRequestedFeature(t *testing.T) {
	for _, policy := range []string{"avx", "simd", "avx,simd"} {
		for _, feature := range strings.Split(policy, ",") {
			base := start(10, "10-1", "initial", 0, "")
			violation := next(base, 2, "cpu_feature_violation")
			violation.CPUFeature = cpuText(feature)
			r := collectREPRows(t, map[int][]event{10: {cpuStart(base, policy), violation}})
			if !r.ValidUnavailableResult() || r.Reason != "disallowed CPU feature: "+feature {
				t.Fatalf("violation not distinguished from normal exit126: %+v", r)
			}
		}
	}
	for name, mutate := range map[string]func(*event, *event){
		"no policy":       func(s, _ *event) { s.CPUPolicyVersion, s.CPUPolicy = nil, nil },
		"wrong feature":   func(_, v *event) { v.CPUFeature = cpuText("simd") },
		"unknown feature": func(_, v *event) { v.CPUFeature = cpuText("avx2") },
		"missing feature": func(_, v *event) { v.CPUFeature = nil },
		"wrong sequence":  func(_, v *event) { v.Seq = 3 },
		"wrong image":     func(_, v *event) { v.Segment = "10-2" },
		"wrong authority": func(_, v *event) { v.Authority = "trusted" },
		"wrong schema":    func(_, v *event) { v.Schema = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			base := start(10, "10-1", "initial", 0, "")
			s := cpuStart(base, "avx")
			v := next(base, 2, "cpu_feature_violation")
			v.CPUFeature = cpuText("avx")
			mutate(&s, &v)
			r := collectREPRows(t, map[int][]event{10: {s, v}})
			if !r.ValidUnavailableResult() || strings.HasPrefix(r.Reason, "disallowed CPU feature:") {
				t.Fatalf("malformed violation became policy evidence: %+v", r)
			}
		})
	}
}

func TestNativeCPUPolicyRejectsMalformedCapabilitiesBeforeViolation(t *testing.T) {
	for _, policy := range []string{"", "AVX", "avx2", "avx,avx", "simd,avx", "avx, simd"} {
		base := start(10, "10-1", "initial", 0, "")
		if r := collectREPRows(t, map[int][]event{10: {cpuStart(base, policy), end(base, 2, 100)}}); !r.ValidUnavailableResult() {
			t.Fatalf("invalid policy %q accepted: %+v", policy, r)
		}
	}
	for name, field := range map[string]string{
		"unknown version":   `"cpu_policy_version":2,"cpu_policy":"avx"`,
		"null version":      `"cpu_policy_version":null,"cpu_policy":"avx"`,
		"boolean version":   `"cpu_policy_version":true,"cpu_policy":"avx"`,
		"fraction version":  `"cpu_policy_version":1.0,"cpu_policy":"avx"`,
		"missing version":   `"cpu_policy":"avx"`,
		"missing policy":    `"cpu_policy_version":1`,
		"null policy":       `"cpu_policy_version":1,"cpu_policy":null`,
		"boolean policy":    `"cpu_policy_version":1,"cpu_policy":true`,
		"duplicate version": `"cpu_policy_version":1,"cpu_policy_version":1,"cpu_policy":"avx"`,
		"duplicate policy":  `"cpu_policy_version":1,"cpu_policy":"avx","cpu_policy":"avx"`,
		"feature on start":  `"cpu_policy_version":1,"cpu_policy":"avx","feature":"avx"`,
	} {
		t.Run(name, func(t *testing.T) {
			base := start(10, "10-1", "initial", 0, "")
			raw, _ := json.Marshal(base)
			last, _ := json.Marshal(end(base, 2, 100))
			dir := t.TempDir()
			data := string(raw[:len(raw)-1]) + "," + field + "}\n" + string(last) + "\n"
			if err := os.WriteFile(filepath.Join(dir, "process-10.jsonl"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if r := CollectDirectory(dir, 10); !r.ValidUnavailableResult() {
				t.Fatalf("malformed policy accepted: %+v", r)
			}
		})
	}
	// Uppercase aliases are unknown scalar fields upstream. They must not
	// silently advertise policy through encoding/json's case-folded decoding.
	base := start(10, "10-1", "initial", 0, "")
	raw, _ := json.Marshal(base)
	aliased := string(raw[:len(raw)-1]) + `,"CPU_POLICY_VERSION":1,"CPU_POLICY":"avx"}`
	row, err := decodeEvent([]byte(aliased))
	if err != nil || row.CPUPolicyVersion != nil || row.CPUPolicy != nil {
		t.Fatalf("case-folded metadata changed policy: %+v, %v", row, err)
	}
}

func TestNativeCPUPolicyReportRejectsInventedOrNoncanonicalMetadata(t *testing.T) {
	for _, raw := range []string{
		`{"disallowed_cpu_features":null}`, `{"disallowed_cpu_features":[]}`,
		`{"disallowed_cpu_features":["avx","avx"]}`, `{"disallowed_cpu_features":["simd","avx"]}`,
		`{"disallowed_cpu_features":["AVX"]}`, `{"disallowed_cpu_features":["avx2"]}`,
		`{"disallowed_cpu_features":[true]}`, `{"DISALLOWED_CPU_FEATURES":["avx"]}`,
		`{"disallowed_cpu_features":["avx"],"Disallowed_CPU_Features":["simd"]}`,
	} {
		var report Report
		if err := json.Unmarshal([]byte(raw), &report); err == nil {
			t.Fatalf("invalid report policy accepted: %s", raw)
		}
	}
}

func TestNativeCPUPolicyLocalStartupReceiptSurvivesGuestFault(t *testing.T) {
	base := start(10, "10-1", "initial", 0, "")
	r := collectREPRows(t, map[int][]event{10: {cpuStart(base, "avx,simd")}})
	if !r.ValidUnavailableResult() || !r.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.SIMD, cpupolicy.AVX}) ||
		r.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX}) || r.HasCPUFeaturePolicy(nil) ||
		r.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX, cpupolicy.AVX}) {
		t.Fatalf("incomplete guest stream lost exact startup policy: %+v", r)
	}
	raw, err := json.Marshal(r)
	if err != nil || strings.Contains(string(raw), "cpu_features") || strings.Contains(string(raw), "CPUFeatures") {
		t.Fatalf("private startup receipt escaped into unavailable JSON: %s, %v", raw, err)
	}
	var remote Report
	if err := json.Unmarshal(raw, &remote); err != nil || remote.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX, cpupolicy.SIMD}) {
		t.Fatalf("remote unavailable JSON acquired a local receipt: %+v, %v", remote, err)
	}
	if r.WithoutTotals("execution interrupted").HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX, cpupolicy.SIMD}) {
		t.Fatal("replacement unavailable report retained local policy evidence")
	}
	violation := next(base, 2, "cpu_feature_violation")
	violation.CPUFeature = cpuText("avx")
	r = collectREPRows(t, map[int][]event{10: {cpuStart(base, "avx,simd"), violation}})
	if r.Reason != "disallowed CPU feature: avx" || !r.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX, cpupolicy.SIMD}) || r.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX}) {
		t.Fatalf("violation cannot prove a different requested policy: %+v", r)
	}
	complete := collectREPRows(t, map[int][]event{10: {cpuStart(base, "avx"), end(base, 2, 100)}})
	complete.DisallowedCPUFeatures[0] = cpupolicy.SIMD
	if !complete.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX}) || complete.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.SIMD}) {
		t.Fatal("public feature slice aliases local startup evidence")
	}
	raw, _ = json.Marshal(complete)
	if err := json.Unmarshal(raw, &remote); err != nil || remote.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.SIMD}) {
		t.Fatalf("remote complete JSON acquired a local receipt: %+v, %v", remote, err)
	}
}

func TestNativeCPUPolicyLocalStartupReceiptRejectsInvalidRootOrMixedPolicy(t *testing.T) {
	for name, mutate := range map[string]func(*event){
		"schema":         func(s *event) { s.Schema = 2 },
		"client version": func(s *event) { s.ClientVersion = 2 },
		"authority":      func(s *event) { s.Authority = "trusted" },
		"metric":         func(s *event) { s.Metric = "other" },
		"PID":            func(s *event) { s.PID = 20 },
		"sequence":       func(s *event) { s.Seq = 2 },
		"SDK":            func(s *event) { s.SDKVersion = "12.0.0" },
		"segment":        func(s *event) { s.Segment = "20-1" },
		"event":          func(s *event) { s.Event = "spawn" },
		"origin":         func(s *event) { s.Origin = "fork" },
		"parent PID":     func(s *event) { s.ParentPID = 20 },
		"parent segment": func(s *event) { s.ParentSegment = "20-1" },
	} {
		t.Run(name, func(t *testing.T) {
			s := cpuStart(start(10, "10-1", "initial", 0, ""), "avx")
			mutate(&s)
			r := collectREPRows(t, map[int][]event{10: {s}})
			if !r.ValidUnavailableResult() || r.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX}) {
				t.Fatalf("invalid root supplied policy startup evidence: %+v", r)
			}
		})
	}
	root := cpuStart(start(10, "10-1", "initial", 0, ""), "avx")
	child := cpuStart(start(20, "20-1", "fork", 10, "10-1"), "simd")
	r := collectREPRows(t, map[int][]event{10: {root}, 20: {child}})
	if !r.ValidUnavailableResult() || r.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX}) || r.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.SIMD}) {
		t.Fatalf("mixed policy retained startup evidence: %+v", r)
	}
}

func TestNativeCPUPolicyLocalStartupReceiptIgnoresMetadataAliases(t *testing.T) {
	for _, pair := range [][2]string{
		{`"schema":1`, `"schema":2,"SCHEMA":1`},
		{`"client_version":1`, `"client_version":2,"CLIENT_VERSION":1`},
		{`"authority":"diagnostic_only"`, `"authority":"trusted","AUTHORITY":"diagnostic_only"`},
		{`"metric":"bb_entry_app_instructions"`, `"metric":"other","METRIC":"bb_entry_app_instructions"`},
		{`"pid":10`, `"pid":20,"PID":10`},
		{`"seq":1`, `"seq":2,"SEQ":1`},
		{`"origin":"initial"`, `"origin":"fork","ORIGIN":"initial"`},
	} {
		root := cpuStart(start(10, "10-1", "initial", 0, ""), "avx")
		raw, _ := json.Marshal(root)
		data := strings.Replace(string(raw), pair[0], pair[1], 1) + "\n"
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "process-10.jsonl"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		r := CollectDirectory(dir, 10)
		if !r.ValidUnavailableResult() || r.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX}) {
			t.Fatalf("case-folded root metadata supplied startup evidence: %s: %+v", data, r)
		}
	}
}
