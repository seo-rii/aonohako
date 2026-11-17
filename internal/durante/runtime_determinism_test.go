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

func TestParseRuntimeDeterminism(t *testing.T) {
	for raw, want := range map[string]string{"": "", "off": "", " v1 ": RuntimeDeterminismV1} {
		if got, err := ParseRuntimeDeterminism(raw); err != nil || got != want {
			t.Fatalf("%q = %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"V1", "v2", "on", "true"} {
		if _, err := ParseRuntimeDeterminism(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestMeasuredRuntimePinsOnlyQualifiedLaunchers(t *testing.T) {
	baseEnv := []string{"PATH=/bin", "PYTHONPATH=/usr/local/lib/aonohako/python", "PYTHONDONTWRITEBYTECODE=1", "HOME=/w", "GLIBC_TUNABLES=x"}
	python := []string{"/usr/bin/python3", "-I", "-S", "-c", "runner", "/w/Main.py"}
	node := []string{"/opt/node22/bin/node", "--stack-size=1024", "/w/main.js"}
	pypy := []string{"/usr/bin/pypy3", "-I", "-S", "-c", "runner", "/w/Main.py"}
	native := []string{"/w/Main"}
	off := Config{DispatchProfile: DispatchDefault}
	on := Config{DispatchProfile: DispatchDefault, RuntimeDeterminism: RuntimeDeterminismV1}
	for _, tc := range []struct {
		runLang string
		argv    []string
	}{{"python", python}, {"javascript", node}, {"pypy", pypy}, {"binary", native}} {
		argv, env, err := off.MeasuredRuntime(tc.runLang, tc.argv, baseEnv)
		if err != nil || !slices.Equal(argv, tc.argv) || !slices.Equal(env, off.RuntimeEnvironment(baseEnv)) || off.RuntimeDeterminismFor(tc.runLang) != "" {
			t.Fatalf("%s changed without runtime determinism: %q %q %v", tc.runLang, argv, env, err)
		}
	}
	for _, runLang := range []string{"pypy", "binary"} {
		argv := map[string][]string{"pypy": pypy, "binary": native}[runLang]
		got, env, err := on.MeasuredRuntime(runLang, argv, baseEnv)
		if err != nil || !slices.Equal(got, argv) || !slices.Equal(env, on.RuntimeEnvironment(baseEnv)) || on.RuntimeDeterminismFor(runLang) != "" {
			t.Fatalf("%s has no qualified options but changed: %q %q %v", runLang, got, env, err)
		}
	}
	pythonBefore, envBefore := slices.Clone(python), slices.Clone(baseEnv)
	argv, env, err := on.MeasuredRuntime("python", python, baseEnv)
	if err != nil || !slices.Equal(argv, []string{"/usr/bin/python3", "-s", "-P", "-S", "-c", "runner", "/w/Main.py"}) {
		t.Fatalf("python argv %q %v", argv, err)
	}
	// -E stays in effect for every variable except the pinned seed.
	if !slices.Equal(env, []string{"PATH=/bin", "HOME=/w", "PYTHONHASHSEED=0"}) || on.RuntimeDeterminismFor("python") != RuntimeDeterminismV1 {
		t.Fatalf("python env %q", env)
	}
	if !slices.Equal(python, pythonBefore) || !slices.Equal(baseEnv, envBefore) {
		t.Fatal("modified caller slices")
	}
	argv, env, err = on.MeasuredRuntime("javascript", node, baseEnv)
	if err != nil || !slices.Equal(argv, []string{"/opt/node22/bin/node", "--predictable", "--stack-size=1024", "/w/main.js"}) || !slices.Equal(env, on.RuntimeEnvironment(baseEnv)) {
		t.Fatalf("node %q %q %v", argv, env, err)
	}
	for _, tc := range []struct {
		runLang string
		argv    []string
	}{
		{"python", []string{"/usr/bin/python3", "-S", "/w/Main.py"}},
		{"python", []string{"/usr/bin/python3.13", "-I", "/w/Main.py"}},
		{"python", []string{"/usr/bin/python3"}},
		{"javascript", []string{"/usr/bin/deno", "run", "/w/main.js"}},
		{"javascript", []string{"node", "--predictable", "/w/main.js"}},
		{"javascript", []string{"node"}},
	} {
		if _, _, err := on.MeasuredRuntime(tc.runLang, tc.argv, baseEnv); err == nil {
			t.Fatalf("pinned an unrecognized launcher %q", tc.argv)
		}
	}
	unknown := Config{DispatchProfile: DispatchDefault, RuntimeDeterminism: "v9"}
	if _, _, err := unknown.MeasuredRuntime("python", python, baseEnv); err == nil {
		t.Fatal("accepted an unknown option set")
	}
}

func TestReportRuntimeDeterminismDecoding(t *testing.T) {
	report := Report{Schema: 1, RuntimeDeterminism: RuntimeDeterminismV1}
	data, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(data), `"runtime_determinism":"v1"`) {
		t.Fatalf("%s %v", data, err)
	}
	var decoded Report
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.RuntimeDeterminism != RuntimeDeterminismV1 {
		t.Fatalf("%+v %v", decoded, err)
	}
	for _, body := range []string{`{"runtime_determinism":""}`, `{"runtime_determinism":"v2"}`, `{"Runtime_Determinism":"v1"}`} {
		if err := json.Unmarshal([]byte(body), &decoded); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	unavailable := incomplete("stopped")
	if !unavailable.ValidUnavailableResult() {
		t.Fatal("fixture is not an unavailable result")
	}
	unavailable.RuntimeDeterminism = RuntimeDeterminismV1
	if unavailable.ValidUnavailableResult() {
		t.Fatal("unavailable result carried an applied option set")
	}
}

func TestJudgeRequiresRuntimeDeterminismForJITAndInterpreterFamilies(t *testing.T) {
	cfg := judgeConfig()
	for _, family := range []string{"cpython", "javascript-wasm"} {
		cfg.Families = append(cfg.Families, family)
		cfg.Judge = JudgePolicy{Enabled: true, Families: []string{family}, Integrity: JudgeIntegrityOff}
		cfg.RuntimeDeterminism = ""
		if err := cfg.Judge.Validate(cfg); err == nil || !strings.Contains(err.Error(), "runtime determinism") {
			t.Fatalf("%s judged without runtime determinism: %v", family, err)
		}
		cfg.RuntimeDeterminism = RuntimeDeterminismV1
		if err := cfg.Judge.Validate(cfg); err != nil {
			t.Fatalf("%s: %v", family, err)
		}
	}
}

func TestSessionRecordsAppliedRuntimeDeterminism(t *testing.T) {
	for _, tc := range []struct {
		name, configured, runLang, want string
		callRuntime, complete           bool
	}{
		{"pinned python", RuntimeDeterminismV1, "python", RuntimeDeterminismV1, true, true},
		{"pinned but native", RuntimeDeterminismV1, "binary", "", true, true},
		{"runtime not applied", RuntimeDeterminismV1, "python", "", false, true},
		{"off", "", "python", "", true, true},
		{"incomplete", RuntimeDeterminismV1, "python", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			launcher := runtimeInstallationFixture(t, root)
			client := filepath.Join(root, "client.so")
			if err := os.WriteFile(client, []byte("synthetic diagnostic client"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := ParseConfig("cpython", launcher, client, DispatchDefault)
			if err != nil {
				t.Fatal(err)
			}
			cfg.RuntimeDeterminism = tc.configured
			s, err := Prepare(context.Background(), root, cfg, "PYTHON3", os.Geteuid(), os.Getegid())
			if err != nil {
				t.Fatal(err)
			}
			if tc.callRuntime {
				argv := []string{"/w/Main"}
				if tc.runLang == "python" {
					argv = []string{"/usr/bin/python3", "-I", "-S", "/w/Main.py"}
				}
				if _, _, err := s.Runtime(tc.runLang, argv, []string{"PATH=/bin"}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.complete {
				writeProfiledTree(t, s.dir, nil)
			}
			r := s.Collect(10)
			if r.Complete != tc.complete || r.RuntimeDeterminism != tc.want {
				t.Fatalf("complete=%v runtime_determinism=%q want %q", r.Complete, r.RuntimeDeterminism, tc.want)
			}
		})
	}
}
