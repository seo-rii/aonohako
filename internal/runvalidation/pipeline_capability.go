package runvalidation

import (
	"slices"

	"aonohako/internal/model"
)

func PipelineV1Contract() model.PipelineV1Capability {
	return model.PipelineV1Capability{
		Versions:        []int{1},
		MaxSteps:        MaxPipelineSteps,
		Executors:       []string{"batch", "interactive"},
		ArtifactSources: []string{"participant_stdout", "participant_file", "interactor_output"},
		ArtifactSourceMaxBytes: map[string]int64{
			"participant_stdout": MaxStepHandoffBytes,
			"participant_file":   MaxPipelineParticipantFileBytes,
			"interactor_output":  MaxStepHandoffBytes,
		},
		FinalJudges: []string{"diff", "spj"},
	}
}

// SupportsPipelineV1Contract checks everything this control plane advertises.
// A worker may support additional versions, executors or larger artifacts.
func SupportsPipelineV1Contract(worker *model.PipelineV1Capability) bool {
	required := PipelineV1Contract()
	if worker == nil || !slices.Contains(worker.Versions, 1) || worker.MaxSteps < required.MaxSteps {
		return false
	}
	for _, kind := range required.Executors {
		if !slices.Contains(worker.Executors, kind) {
			return false
		}
	}
	for _, source := range required.ArtifactSources {
		if !slices.Contains(worker.ArtifactSources, source) || worker.ArtifactSourceMaxBytes[source] < required.ArtifactSourceMaxBytes[source] {
			return false
		}
	}
	for _, judge := range required.FinalJudges {
		if !slices.Contains(worker.FinalJudges, judge) {
			return false
		}
	}
	return true
}
