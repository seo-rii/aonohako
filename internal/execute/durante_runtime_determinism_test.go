package execute

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/pythonpolicy"
)

func TestRuntimeDeterminismPinsDescriptorAndChangesIdentity(t *testing.T) {
	off := durante.Config{DispatchProfile: durante.DispatchDefault}
	on := durante.Config{DispatchProfile: durante.DispatchDefault, RuntimeDeterminism: durante.RuntimeDeterminismV1}
	for _, tc := range []struct {
		lang, mode string
		pinned     bool
	}{
		{"JAVASCRIPT", "", true}, {"PYTHON3", "", true}, {"PYTHON3", "installed", true},
		{"PYPY3", "", false}, {"CPP20", "", false},
	} {
		req := &model.RunRequest{Lang: tc.lang, PythonLibraryMode: pythonpolicy.LibraryMode(tc.mode), Limits: model.Limits{MemoryMB: 256}}
		before, err := effectiveRuntimeOptions(req, off, config.DefaultRuntimeTuningConfig())
		if err != nil {
			t.Fatal(err)
		}
		after, err := effectiveRuntimeOptions(req, on, config.DefaultRuntimeTuningConfig())
		if err != nil {
			t.Fatal(err)
		}
		beforeID, _ := before.ID()
		afterID, _ := after.ID()
		if !tc.pinned {
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("%s has no qualified options but its descriptor changed", tc.lang)
			}
			continue
		}
		if beforeID == afterID {
			t.Fatalf("%s descriptor identity ignored runtime determinism", tc.lang)
		}
		switch tc.lang {
		case "JAVASCRIPT":
			if !slices.Equal(after.Argv, slices.Insert(slices.Clone(before.Argv), 1, "--predictable")) || !slices.Equal(after.Env, before.Env) {
				t.Fatalf("node descriptor %q", after.Argv)
			}
		case "PYTHON3":
			if after.Argv[1] != "-s" || after.Argv[2] != "-P" || slices.Contains(after.Argv, "-I") || !slices.Equal(after.Argv[3:], before.Argv[2:]) {
				t.Fatalf("python descriptor %q", after.Argv)
			}
			for _, entry := range after.Env {
				if strings.HasPrefix(entry, "PYTHON") && entry != "PYTHONHASHSEED=0" {
					t.Fatalf("python descriptor kept %s", entry)
				}
			}
			if !slices.Contains(after.Env, "PYTHONHASHSEED=0") {
				t.Fatalf("python descriptor env %q", after.Env)
			}
		}
	}
}

// A model qualified without the pinned options cannot convert a pinned
// observation, and a pinned binding requires the runner's receipt.
func TestRuntimeDeterminismBindsTimeConversion(t *testing.T) {
	req := timeMeasurementRequest()
	req.Lang = "PYTHON3"
	unpinned := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{}).Execution.WorkMeasurement
	pinned := unpinned
	pinned.RuntimeDeterminism = durante.RuntimeDeterminismV1
	report := completeEnvelopeReport()
	report.Family = "cpython"
	local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, report), pinned)
	if d := local.WorkMeasurementExecution; d.TimeEstimate != nil || d.TimeEstimateUnavailable == "" {
		t.Fatalf("unpinned qualification converted a pinned run: %+v", d)
	}

	pinned.TimeModels = pinned.TimeModels.Clone()
	pinned.TimeModels.Profiles = nil
	descriptions, err := DescribeWorkMeasurementRuntimeOptions(req, config.ExecutionConfig{WorkMeasurement: pinned})
	if err != nil {
		t.Fatal(err)
	}
	options := descriptions[0].RuntimeOptions
	pinned.TimeModels.Profiles = []durante.TimeProfileBinding{{
		SourceLanguage: "PYTHON3", Family: "cpython", RuntimeImage: pinned.RuntimeImage,
		EngineProfile: durante.EngineProfile, DispatchProfile: pinned.DispatchProfile,
		ModelID: pinned.TimeModels.Models[0].ModelID, ModelVersion: 1, QualificationID: "synthetic-pinned",
		RuntimeOptions: &options,
	}}
	if err := pinned.ValidateTimeConfiguration(); err != nil {
		t.Fatal(err)
	}
	local = finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, report), pinned)
	if d := local.WorkMeasurementExecution; d.TimeEstimate != nil || d.TimeEstimateEligibility == nil || !slices.Equal(d.TimeEstimateEligibility.ReasonCodes, []string{durante.TimeRuntimeProfileMismatch}) {
		t.Fatalf("observation without the runner receipt converted: %+v", d)
	}
	report.RuntimeDeterminism = durante.RuntimeDeterminismV1
	local = finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, report), pinned)
	estimate := local.WorkMeasurementExecution.TimeEstimate
	if estimate == nil || estimate.RuntimeDeterminism != durante.RuntimeDeterminismV1 || estimate.RuntimeOptionsID != descriptions[0].RuntimeOptionsID {
		t.Fatalf("pinned observation did not convert: %+v", local.WorkMeasurementExecution)
	}
	if remote := exchangeTimeMeasurementResult(t, req, local, pinned); !reflect.DeepEqual(remote, local) {
		t.Fatalf("pinned estimate did not survive HTTP/SSE: %+v", remote)
	}
}

func TestRuntimeDeterminismRejectsSchemaOneBindingsForPinnedLaunchers(t *testing.T) {
	cfg := sharedTimeMeasurementConfig()
	cfg.RuntimeDeterminism = durante.RuntimeDeterminismV1
	if err := cfg.ValidateTimeConfiguration(); err == nil || !strings.Contains(err.Error(), "schema 2") {
		t.Fatalf("schema 1 PYTHON3 binding accepted under runtime determinism: %v", err)
	}
	var kept []durante.TimeProfileBinding
	for _, binding := range cfg.TimeModels.Profiles {
		if binding.SourceLanguage != "PYTHON3" {
			kept = append(kept, binding)
		}
	}
	cfg.TimeModels.Profiles = kept
	if err := cfg.ValidateTimeConfiguration(); err != nil {
		t.Fatalf("unpinned schema 1 bindings rejected: %v", err)
	}
}
