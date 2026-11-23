package execute

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func boundRuntimeOptionsConfig(t *testing.T, req *model.RunRequest, execution config.ExecutionConfig) config.Config {
	t.Helper()
	measurement := sharedTimeMeasurementConfig()
	measurement.Families = durante.RegisteredFamilies()
	measurement.TimeModels.Schema = 2
	measurement.TimeModels.Profiles = nil
	execution.WorkMeasurement = measurement
	descriptions, err := DescribeWorkMeasurementRuntimeOptions(req, execution)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, description := range descriptions {
		key := description.SourceLanguage + description.RuntimeOptionsID
		if seen[key] {
			continue
		}
		seen[key] = true
		family, _ := durante.FamilyForLanguage(description.SourceLanguage)
		options := description.RuntimeOptions
		measurement.TimeModels.Profiles = append(measurement.TimeModels.Profiles, durante.TimeProfileBinding{
			SourceLanguage: description.SourceLanguage, Family: family,
			RuntimeImage: measurement.RuntimeImage, EngineProfile: durante.EngineProfile, DispatchProfile: measurement.DispatchProfile,
			ModelID: measurement.TimeModels.Models[0].ModelID, ModelVersion: 1, QualificationID: "synthetic-options-only",
			RuntimeOptions: &options,
		})
	}
	if err := measurement.ValidateTimeConfiguration(); err != nil {
		t.Fatal(err)
	}
	execution.WorkMeasurement = measurement
	return config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: execution}
}

func TestRuntimeOptionsUseEffectiveRawMemoryAndResolvedTuning(t *testing.T) {
	req := timeMeasurementRequest()
	req.Lang, req.Limits.MemoryMB = "JAVASCRIPT", 64
	req.WorkMeasurementLimits.MemoryMB = 256
	defaultTuning := config.DefaultRuntimeTuningConfig()
	named := defaultTuning
	named.NodeOldSpacePercent = 75
	execution := config.ExecutionConfig{RuntimeTuning: defaultTuning, RuntimeTuningProfiles: map[string]config.RuntimeTuningConfig{"same": defaultTuning, "larger": named}}
	describe := func() WorkMeasurementRuntimeOptionsDescription {
		t.Helper()
		values, err := DescribeWorkMeasurementRuntimeOptions(req, execution)
		if err != nil || len(values) != 1 {
			t.Fatalf("describe: %+v %v", values, err)
		}
		return values[0]
	}
	first := describe()
	if first.RuntimeOptions.MemoryMB != 256 || !slices.Contains(first.RuntimeOptions.Argv, "--max-old-space-size=153") || !slices.Contains(first.RuntimeOptions.Argv, "--max-semi-space-size=4") {
		t.Fatalf("nominal memory replaced raw runtime options: %+v", first)
	}
	req.Limits.MemoryMB = 32
	req.RuntimeProfile = "same"
	req.Stdin, req.EntryPoint = "different program input", "other-target.js"
	if same := describe(); same.RuntimeOptionsID != first.RuntimeOptionsID {
		t.Fatal("nominal limit, profile name or program input changed equivalent runtime options")
	}
	req.WorkMeasurementLimits.MemoryMB = 512
	second := describe()
	if second.RuntimeOptionsID == first.RuntimeOptionsID || !slices.Contains(second.RuntimeOptions.Argv, "--max-old-space-size=307") || !slices.Contains(second.RuntimeOptions.Argv, "--max-semi-space-size=8") {
		t.Fatalf("raw memory failed to change actual Node flags: %+v", second)
	}
	req.RuntimeProfile = "larger"
	third := describe()
	if third.RuntimeOptionsID == second.RuntimeOptionsID || !slices.Contains(third.RuntimeOptions.Argv, "--max-old-space-size=384") {
		t.Fatalf("effective named tuning did not change Node flags: %+v", third)
	}
	req.RuntimeProfile = ""
	execution.RuntimeTuning = named
	if describe().RuntimeOptionsID != third.RuntimeOptionsID {
		t.Fatal("identical default and named tuning produced different identities")
	}
	// The generated argv is the same command builder output used by execution.
	raw := workMeasurementRawRequest(req)
	want := buildCommandWithRuntimeTuning(runtimeOptionsProgram, "javascript", &raw, named)
	if !reflect.DeepEqual(third.RuntimeOptions.Argv, want) {
		t.Fatal("descriptor diverged from real command generation")
	}
}

func TestRuntimeOptionsPythonModeAndDispatchUseActualWrapperAndEnvironment(t *testing.T) {
	for _, language := range []string{"PYTHON3", "PYPY3"} {
		req := &model.RunRequest{Lang: language, Limits: model.Limits{MemoryMB: 256}}
		cfg := durante.Config{DispatchProfile: durante.DispatchDefault}
		stdlib, err := effectiveRuntimeOptions(req, cfg, config.DefaultRuntimeTuningConfig())
		if err != nil {
			t.Fatal(err)
		}
		req.PythonLibraryMode = "stdlib"
		explicit, _ := effectiveRuntimeOptions(req, cfg, config.DefaultRuntimeTuningConfig())
		if !reflect.DeepEqual(stdlib, explicit) || !slices.Contains(stdlib.Argv, pythonStdlibRunner) {
			t.Fatal("stdlib defaults or actual wrapper were lost")
		}
		req.PythonLibraryMode = "installed"
		installed, _ := effectiveRuntimeOptions(req, cfg, config.DefaultRuntimeTuningConfig())
		stdlibID, _ := stdlib.ID()
		installedID, _ := installed.ID()
		if installedID == stdlibID || !slices.Contains(installed.Argv, pythonInstalledRunner) || !slices.Contains(installed.Argv, pythonTrustedSitecustomizePath) {
			t.Fatal("installed Python wrapper was not bound")
		}
	}
	req := &model.RunRequest{Lang: "CPP20", Limits: model.Limits{MemoryMB: 256}}
	plain, _ := effectiveRuntimeOptions(req, durante.Config{DispatchProfile: durante.DispatchDefault}, config.DefaultRuntimeTuningConfig())
	portableConfig := durante.Config{DispatchProfile: durante.DispatchGLIBC241}
	portable, _ := effectiveRuntimeOptions(req, portableConfig, config.DefaultRuntimeTuningConfig())
	want := portableConfig.RuntimeEnvironment(sandboxBaseEnvironment(Workspace{RootDir: runtimeOptionsWorkspace}, req))
	slices.Sort(want)
	if !reflect.DeepEqual(portable.Env, want) || reflect.DeepEqual(portable.Env, plain.Env) {
		t.Fatal("measurement dispatch environment diverged from execution")
	}
}

func TestRuntimeOptionsMismatchRejectsBeforeLocalExecutionOrUpload(t *testing.T) {
	for _, change := range []string{"raw memory", "operator tuning", "Python library"} {
		t.Run(change, func(t *testing.T) {
			req := timeMeasurementRequest()
			req.Lang = "JAVASCRIPT"
			if change == "Python library" {
				req.Lang = "PYTHON3"
			}
			cfg := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{})
			switch change {
			case "raw memory":
				req.WorkMeasurementLimits.MemoryMB = 256
			case "operator tuning":
				cfg.Execution.RuntimeTuning = config.DefaultRuntimeTuningConfig()
				cfg.Execution.RuntimeTuning.NodeOldSpacePercent = 75
			case "Python library":
				req.PythonLibraryMode = "installed"
			}
			var called atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called.Store(true)
				http.Error(w, "unexpected upload", 500)
			}))
			defer server.Close()
			cfg.Execution.Remote.URL = server.URL
			for name, runner := range map[string]Runner{"local": NewWithConfig(cfg), "remote": newRemoteRunner(cfg)} {
				got := runner.Run(context.Background(), req, Hooks{})
				if got.Status != model.RunStatusInitFail || got.WorkMeasurementExecution != nil || !strings.Contains(got.Reason, "no approved profile") {
					t.Fatalf("%s admitted mismatching runtime: %+v", name, got)
				}
			}
			if called.Load() {
				t.Fatal("mismatching options were uploaded")
			}
		})
	}
}

func TestRuntimeOptionsRemoteSSERequiresExactBindingIdentity(t *testing.T) {
	for _, language := range []string{"CPP20", "JAVASCRIPT", "PYTHON3", "PYPY3"} {
		req := timeMeasurementRequest()
		req.Lang = language
		cfg := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{})
		report := completeEnvelopeReport()
		report.Family, _ = durante.FamilyForLanguage(language)
		local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, report), cfg.Execution.WorkMeasurement)
		if local.WorkMeasurementExecution.TimeEstimate == nil || local.WorkMeasurementExecution.TimeEstimate.RuntimeOptionsID == "" {
			t.Fatalf("bound estimate missing for %s: %+v", language, local)
		}
		if got := exchangeTimeMeasurementResult(t, req, local, cfg.Execution.WorkMeasurement); !reflect.DeepEqual(got, local) {
			t.Fatalf("valid bound SSE result changed: %+v", got)
		}
		for _, invalidID := range []string{"", "runtime-options-v1:sha256:" + strings.Repeat("f", 64)} {
			changed := *local.WorkMeasurementExecution.TimeEstimate
			changed.RuntimeOptionsID = invalidID
			local.WorkMeasurementExecution.TimeEstimate = &changed
			got := exchangeTimeMeasurementResult(t, req, local, cfg.Execution.WorkMeasurement)
			if got.WorkMeasurementExecution != nil || !strings.Contains(got.Reason, "invalid remote result") {
				t.Fatal("old or mismatched options identity was accepted")
			}
		}
	}
}

func TestRuntimeOptionsPeersUseActualRoleLimitsAndRejectSwappedIdentity(t *testing.T) {
	for _, mode := range []string{"interactive", "communication-v1", "communication-v2"} {
		t.Run(mode, func(t *testing.T) {
			req, response := peerTimeFixture(t, mode)
			req.Limits = model.Limits{TimeMs: 10, MemoryMB: 64}
			if req.Interactor != nil {
				req.Interactor.Limits = &model.Limits{TimeMs: 10, MemoryMB: 256}
			}
			cfg := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{})
			descriptions, err := DescribeWorkMeasurementRuntimeOptions(req, cfg.Execution)
			if err != nil {
				t.Fatal(err)
			}
			last := descriptions[len(descriptions)-1]
			wantMemory := communicationManagerMemoryMB
			if mode == "interactive" {
				wantMemory = 256
			}
			if descriptions[0].RuntimeOptions.MemoryMB != 64 || last.RuntimeOptions.MemoryMB != wantMemory || descriptions[0].RuntimeOptionsID == last.RuntimeOptionsID {
				t.Fatal("role limits collapsed into top-level limits")
			}
			local := finishWorkMeasurementPeerTime(req, response, cfg.Execution.WorkMeasurement)
			for i, role := range local.WorkMeasurements {
				if role.TimeBudget == nil || role.TimeBudget.Estimate == nil || role.TimeBudget.Estimate.RuntimeOptionsID != descriptions[i].RuntimeOptionsID {
					t.Fatalf("wrong bound role estimate: %+v", role)
				}
			}
			if got := peerTimeExchange(t, req, local, cfg.Execution.WorkMeasurement, true); !reflect.DeepEqual(got, local) {
				t.Fatal("bound peer SSE round trip changed")
			}
			local.WorkMeasurements[0].TimeBudget.Estimate.RuntimeOptionsID = last.RuntimeOptionsID
			if got := peerTimeExchange(t, req, local, cfg.Execution.WorkMeasurement, true); !strings.Contains(got.Reason, "invalid remote result") {
				t.Fatal("another role's descriptor was accepted")
			}
			if req.Interactor != nil {
				req.Interactor.Limits.MemoryMB++
			} else {
				req.Limits.MemoryMB++
			}
			if _, err := workMeasurementPeerTimePlans(req, cfg.Execution.WorkMeasurement); err == nil {
				t.Fatal("changed role limit reused an old binding")
			}
		})
	}
}

func TestRuntimeOptionsSnapshotsAndUnsupportedLaunchers(t *testing.T) {
	req := timeMeasurementRequest()
	req.Lang, req.RuntimeProfile = "JAVASCRIPT", "selected"
	tuning := config.DefaultRuntimeTuningConfig()
	tuning.NodeOldSpacePercent = 75
	cfg := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{RuntimeTuningProfiles: map[string]config.RuntimeTuningConfig{"selected": tuning}})
	service, remote := NewWithConfig(cfg), newRemoteRunner(cfg).(*remoteRunner)
	cfg.Execution.RuntimeTuningProfiles["selected"] = config.DefaultRuntimeTuningConfig()
	cfg.Execution.WorkMeasurement.TimeModels.Profiles[0].RuntimeOptions.Argv[1] = "changed"
	for _, execution := range []config.ExecutionConfig{
		{WorkMeasurement: service.workMeasurement, RuntimeTuning: service.runtimeTuning, RuntimeTuningProfiles: service.runtimeTuningProfiles},
		{WorkMeasurement: remote.workMeasurement, RuntimeTuning: remote.runtimeTuning, RuntimeTuningProfiles: remote.runtimeTuningProfiles},
	} {
		selected, err := resolveRuntimeTuning(req, execution.RuntimeTuning, execution.RuntimeTuningProfiles)
		if err != nil || selected.NodeOldSpacePercent != 75 {
			t.Fatal("runtime tuning snapshot changed", err)
		}
		if _, _, err := ordinaryTimePolicy(req, execution.WorkMeasurement, selected); err != nil {
			t.Fatal("descriptor snapshot changed", err)
		}
	}
	for _, source := range []string{"JAVASCRIPT_DENO", "JAVASCRIPT_BUN", "JAVA21", "RUBY"} {
		req.Lang, req.RuntimeProfile = source, ""
		if _, err := DescribeWorkMeasurementRuntimeOptions(req, config.ExecutionConfig{}); err == nil {
			t.Fatalf("unsupported source %s claimed an options descriptor", source)
		}
	}
}
