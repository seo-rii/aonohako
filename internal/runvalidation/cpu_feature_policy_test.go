package runvalidation

import (
	"strings"
	"testing"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func cpuPolicyBatchRequest() *model.RunRequest {
	return &model.RunRequest{
		Lang: "binary", Binaries: []model.Binary{{Name: "Main", DataB64: "eA==", Mode: "exec"}},
		Limits:                model.Limits{TimeMs: 1000, MemoryMB: 64},
		DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.AVX},
	}
}

func cpuPolicyLegacyRequest() *model.RunRequest {
	req := validTwoStepPipelineRequest()
	req.DisallowedCPUFeatures = []cpupolicy.Feature{cpupolicy.SIMD}
	return req
}

func cpuPolicyPipelineRequest() *model.RunRequest {
	req := validPipelineRequest()
	req.DisallowedCPUFeatures = []cpupolicy.Feature{cpupolicy.AVX}
	req.Pipeline.Programs[0].Lang = "binary"
	return req
}

func TestValidateDisallowedCPUFeaturesAllowsSupportedContestants(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func() *model.RunRequest
		edit func(*model.RunRequest)
	}{
		{name: "batch", make: cpuPolicyBatchRequest},
		{name: "native alias", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) { req.Lang = "CPP17" }},
		{name: "both features", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) {
			req.DisallowedCPUFeatures = []cpupolicy.Feature{cpupolicy.SIMD, cpupolicy.AVX}
		}},
		{name: "omitted policy keeps other runtimes", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) {
			req.DisallowedCPUFeatures = nil
			req.Lang = "python"
		}},
		{name: "empty policy keeps file outputs", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) {
			req.DisallowedCPUFeatures = []cpupolicy.Feature{}
			req.FileOutputs = []model.OutputFile{{Path: "answer.txt"}}
		}},
		{name: "omitted policy keeps Durante", make: validDuranteLimitsRequest},
		{name: "empty policy keeps Durante", make: validDuranteLimitsRequest, edit: func(req *model.RunRequest) {
			req.DisallowedCPUFeatures = []cpupolicy.Feature{}
		}},
		{name: "interactive trusted Python peer", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) {
			req.Interactor = &model.InteractorSpec{Lang: "python", Binaries: []model.Binary{{Name: "interactor.py", DataB64: "eA=="}}}
		}},
		{name: "trusted Python SPJ with sidecar", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) {
			req.SPJ = &model.SPJSpec{Lang: "python", Binary: &model.Binary{Name: "checker.py", DataB64: "eA=="}, SidecarOutputs: []model.OutputFile{{Path: "judge.json"}}}
		}},
		{name: "legacy stdin stdout steps", make: cpuPolicyLegacyRequest},
		{name: "pipeline interactor output", make: cpuPolicyPipelineRequest},
		{name: "pipeline trusted Python SPJ", make: cpuPolicyPipelineRequest, edit: func(req *model.RunRequest) {
			req.Pipeline.FinalJudge.Kind = "spj"
			req.Pipeline.FinalJudge.SPJ = &model.SPJSpec{Lang: "python", Binary: &model.Binary{Name: "checker.py", DataB64: "eA=="}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.make()
			if tc.edit != nil {
				tc.edit(req)
			}
			if err := Validate(req); err != nil {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

func TestValidateDisallowedCPUFeaturesRejectsUnsupportedRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func() *model.RunRequest
		edit func(*model.RunRequest)
		want string
	}{
		{name: "unknown option", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) { req.DisallowedCPUFeatures = []cpupolicy.Feature{"avx2"} }, want: "invalid disallowed_cpu_features"},
		{name: "noncanonical option", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) { req.DisallowedCPUFeatures = []cpupolicy.Feature{"AVX"} }, want: "invalid disallowed_cpu_features"},
		{name: "duplicate option", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) {
			req.DisallowedCPUFeatures = []cpupolicy.Feature{cpupolicy.AVX, cpupolicy.AVX}
		}, want: "invalid disallowed_cpu_features"},
		{name: "too many options", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) {
			req.DisallowedCPUFeatures = []cpupolicy.Feature{cpupolicy.AVX, cpupolicy.SIMD, cpupolicy.AVX}
		}, want: "invalid disallowed_cpu_features"},
		{name: "Durante measurement", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) {
			req.Lang = "CPP20"
			req.WorkMeasurement = true
		}, want: "disallowed_cpu_features does not support work_measurement"},
		{name: "Durante resource envelope", make: validDuranteLimitsRequest, edit: func(req *model.RunRequest) {
			req.DisallowedCPUFeatures = []cpupolicy.Feature{cpupolicy.AVX}
		}, want: "disallowed_cpu_features does not support work_measurement"},
		{name: "Durante limits without measurement", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) {
			req.Lang = "CPP20"
			req.WorkMeasurementLimits = &durante.ResourceLimits{CPUTimeMs: 2000, WallTimeMs: 4000, MemoryMB: 128}
		}, want: "disallowed_cpu_features does not support work_measurement"},
		{name: "Durante interactive measurement", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) {
			req.Lang = "CPP20"
			req.WorkMeasurement = true
			req.Interactor = &model.InteractorSpec{Lang: "CPP20", Binaries: []model.Binary{{Name: "interactor", DataB64: "eA==", Mode: "exec"}}}
		}, want: "disallowed_cpu_features does not support work_measurement"},
		{name: "Durante legacy steps", make: cpuPolicyLegacyRequest, edit: func(req *model.RunRequest) {
			req.WorkMeasurement = true
		}, want: "disallowed_cpu_features does not support work_measurement"},
		{name: "Durante pipeline", make: cpuPolicyPipelineRequest, edit: func(req *model.RunRequest) {
			req.WorkMeasurement = true
		}, want: "disallowed_cpu_features does not support work_measurement"},
		{name: "managed contestant", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) { req.Lang = "PYTHON3" }, want: "native binary contestant"},
		{name: "different native runtime", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) { req.Lang = "go-binary" }, want: "native binary contestant"},
		{name: "network", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) { req.EnableNetwork = true }, want: "enable_network"},
		{name: "file output", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) { req.FileOutputs = []model.OutputFile{{Path: "answer"}} }, want: "file_outputs"},
		{name: "sidecar output", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) { req.SidecarOutputs = []model.OutputFile{{Path: "image"}} }, want: "sidecar_outputs"},
		{name: "communication", make: cpuPolicyBatchRequest, edit: func(req *model.RunRequest) { req.Communication = &model.CommunicationSpec{} }, want: "communication"},
		{name: "legacy unsupported program", make: cpuPolicyLegacyRequest, edit: func(req *model.RunRequest) { req.Programs[1].Lang = "python" }, want: "native binary contestant"},
		{name: "legacy program network", make: cpuPolicyLegacyRequest, edit: func(req *model.RunRequest) { req.Programs[1].EnableNetwork = true }, want: "enable_network"},
		{name: "legacy file handoff", make: cpuPolicyLegacyRequest, edit: func(req *model.RunRequest) { req.Steps[0].Handoff.From = "file"; req.Steps[0].Handoff.Path = "out" }, want: "file handoff"},
		{name: "legacy file_output alias", make: cpuPolicyLegacyRequest, edit: func(req *model.RunRequest) {
			req.Steps[0].Handoff.From = " FILE_OUTPUT "
			req.Steps[0].Handoff.Path = "out"
		}, want: "file handoff"},
		{name: "pipeline participant language", make: cpuPolicyPipelineRequest, edit: func(req *model.RunRequest) { req.Pipeline.Programs[0].Lang = "python" }, want: "native binary contestant"},
		{name: "pipeline batch contestant formerly interactor", make: cpuPolicyPipelineRequest, edit: func(req *model.RunRequest) { req.Pipeline.Steps[1].Executor.ProgramID = "interactor" }, want: "native binary contestant"},
		{name: "pipeline participant network", make: cpuPolicyPipelineRequest, edit: func(req *model.RunRequest) { req.Pipeline.Programs[0].EnableNetwork = true }, want: "enable_network"},
		{name: "pipeline participant file output", make: cpuPolicyPipelineRequest, edit: func(req *model.RunRequest) {
			req.Pipeline.Steps[0].Outputs[0].Source = model.PipelineOutputSource{Kind: "participant_file", Path: "answer"}
		}, want: "participant_file"},
		{name: "pipeline unknown contestant", make: cpuPolicyPipelineRequest, edit: func(req *model.RunRequest) { req.Pipeline.Steps[0].Executor.ParticipantProgramID = "missing" }, want: "unknown contestant"},
		{name: "pipeline unknown executor", make: cpuPolicyPipelineRequest, edit: func(req *model.RunRequest) { req.Pipeline.Steps[0].Executor.Kind = "unknown" }, want: "batch or interactive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.make()
			tc.edit(req)
			for name, validate := range map[string]func(*model.RunRequest) error{"dedicated": ValidateDisallowedCPUFeatures, "full": Validate} {
				if err := validate(req); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("%s validation = %v, want %q", name, err, tc.want)
				}
			}
		})
	}
	if err := ValidateDisallowedCPUFeatures(nil); err == nil {
		t.Fatal("nil request passed CPU policy validation")
	}
}
