package execute

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func sharedTimeMeasurementConfig() durante.Config {
	cfg := timeMeasurementConfig()
	cfg.TimePolicy = nil
	cfg.Families = []string{"cpp", "cpython", "pypy", "native"}
	cfg.TimeModels = &durante.TimeModels{Schema: 1, Models: []durante.TimeModel{{
		ModelID: "shared-synthetic", ModelVersion: 1, ReferenceID: "unit-only",
		Coefficients: durante.TimeCoefficients{InstructionNSNumerator: 1, REPByteNSNumerator: 1, NSDenominator: 1},
	}}, Profiles: []durante.TimeProfileBinding{}}
	for _, source := range []string{"CPP20", "PYTHON3", "PYPY3", "RUST2021"} {
		family, _ := durante.FamilyForLanguage(source)
		cfg.TimeModels.Profiles = append(cfg.TimeModels.Profiles, durante.TimeProfileBinding{
			SourceLanguage: source, Family: family, RuntimeImage: cfg.RuntimeImage,
			EngineProfile: durante.EngineProfile, DispatchProfile: durante.DispatchDefault,
			ModelID: "shared-synthetic", ModelVersion: 1, QualificationID: "unit-" + source,
		})
	}
	return cfg
}

func TestSharedTimeModelsLocalAndRemoteKeepDiagnosticContract(t *testing.T) {
	cfg := sharedTimeMeasurementConfig()
	for _, binding := range cfg.TimeModels.Profiles {
		t.Run(binding.SourceLanguage, func(t *testing.T) {
			req := timeMeasurementRequest()
			req.Lang = binding.SourceLanguage
			report := completeEnvelopeReport()
			report.Family = binding.Family
			before := timeMeasurementDiagnostic(req, model.RunStatusWA, report)
			local := finishWorkMeasurementTime(req, before, cfg)
			estimate := local.WorkMeasurementExecution.TimeEstimate
			if estimate == nil || estimate.Nanoseconds != 1234 || estimate.ModelID != "shared-synthetic" || estimate.Family != binding.Family || estimate.RuntimeID != req.Lang+"@"+cfg.RuntimeImage {
				t.Fatalf("shared model failed to resolve: %+v", local.WorkMeasurementExecution)
			}
			if local.Status != model.RunStatusInitFail || local.Score != nil || estimate.Authority != durante.Authority || local.WorkMeasurementExecution.OutputMatches == nil || *local.WorkMeasurementExecution.OutputMatches || local.CPUTimeNs != before.CPUTimeNs {
				t.Fatalf("model selection changed raw accounting or granted a verdict: %+v", local)
			}
			remote := exchangeTimeMeasurementResult(t, req, local, cfg)
			if !reflect.DeepEqual(local, remote) {
				t.Fatalf("shared model did not survive HTTP/SSE: %+v", remote)
			}
		})
	}
}

func TestSharedTimeModelsRetainCounterQualityRequirements(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		req, cfg := timeMeasurementRequest(), sharedTimeMeasurementConfig()
		req.Lang = "PYTHON3"
		report := completeEnvelopeReport()
		report.Family = "cpython"
		if interrupted {
			report.SignalsObserved, report.Quality = 1, "signals_observed"
		} else {
			unsupported := uint64(1)
			report.UnsupportedREPEntries, report.REPTelemetryStatus = &unsupported, durante.REPPartial
		}
		local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, report), cfg)
		d := local.WorkMeasurementExecution
		if !local.WorkMeasurement.Complete || d.TimeEstimate != nil || d.TimeEstimateUnavailable != "observation_not_convertible" || d.NominalTimeExceeded != nil {
			t.Fatalf("shared model relaxed report quality: %+v", d)
		}
		if remote := exchangeTimeMeasurementResult(t, req, local, cfg); !reflect.DeepEqual(remote, local) {
			t.Fatal("remote did not preserve unavailable estimate")
		}
	}
}

func TestSharedTimeModelsRejectUnapprovedProfileBeforeExecution(t *testing.T) {
	for name, edit := range map[string]func(*durante.Config){
		"catalog without approvals": func(c *durante.Config) { c.TimeModels.Profiles = []durante.TimeProfileBinding{} },
		"source not approved":       func(c *durante.Config) { c.TimeModels.Profiles = c.TimeModels.Profiles[:1] },
		"another image":             func(c *durante.Config) { c.RuntimeImage = "sha256:" + strings.Repeat("b", 64) },
		"family disabled":           func(c *durante.Config) { c.Families = []string{"cpp"} },
	} {
		t.Run(name, func(t *testing.T) {
			measurement := sharedTimeMeasurementConfig()
			edit(&measurement)
			var called atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called.Store(true)
				http.Error(w, "unexpected upload", http.StatusInternalServerError)
			}))
			defer server.Close()
			cfg := config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: config.ExecutionConfig{
				WorkMeasurement: measurement, Remote: config.RemoteExecutorConfig{URL: server.URL},
			}}
			req := timeMeasurementRequest()
			req.Lang = "PYTHON3"
			for runnerName, runner := range map[string]Runner{
				"local": NewWithConfig(cfg), "remote": newRemoteRunner(cfg),
			} {
				got := runner.Run(context.Background(), req, Hooks{})
				if got.Status != model.RunStatusInitFail || got.WorkMeasurementExecution != nil || (!strings.Contains(got.Reason, "no approved profile") && !strings.Contains(got.Reason, "not enabled")) {
					t.Fatalf("%s did not reject profile admission: %+v", runnerName, got)
				}
			}
			if called.Load() {
				t.Fatal("unapproved profile was uploaded")
			}
		})
	}
}

func TestSharedTimeModelRemoteRejectsDifferentModel(t *testing.T) {
	cfg, req := sharedTimeMeasurementConfig(), timeMeasurementRequest()
	req.Lang = "PYTHON3"
	report := completeEnvelopeReport()
	report.Family = "cpython"
	local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, report), cfg)
	cfg.TimeModels.Models[0].Coefficients.InstructionNSNumerator++
	remote := exchangeTimeMeasurementResult(t, req, local, cfg)
	if remote.WorkMeasurementExecution != nil || !strings.Contains(remote.Reason, "invalid remote result") {
		t.Fatalf("front end accepted another shared model: %+v", remote)
	}
}

func TestSharedTimeModelConstructorsFreezeModelsAndBindings(t *testing.T) {
	measurement := sharedTimeMeasurementConfig()
	cfg := config.Config{MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20, Execution: config.ExecutionConfig{WorkMeasurement: measurement}}
	service := NewWithConfig(cfg)
	remote := newRemoteRunner(cfg).(*remoteRunner)
	measurement.TimeModels.Models[0].Coefficients.InstructionNSNumerator = 100
	measurement.TimeModels.Profiles[1].QualificationID = ""
	measurement.TimeModels.Profiles[1].SourceLanguage = "JAVA21"
	for _, frozen := range []durante.Config{service.workMeasurement, remote.workMeasurement} {
		policy, runtimeID, err := frozen.TimePolicyFor("PYTHON3")
		if err != nil || policy.InstructionNSNumerator != 1 || runtimeID != "PYTHON3@"+measurement.RuntimeImage {
			t.Fatalf("constructor retained mutable registry state: %+v %q %v", policy, runtimeID, err)
		}
	}
}
