package model

const (
	PipelineV1WorkerPath            = "/internal/capabilities/pipeline-v1"
	PipelineV1WorkerProtocol        = "aonohako-pipeline-worker-v1"
	PipelineV1WorkerHeader          = "X-Aonohako-Pipeline-Worker"
	PipelineObservationBudgetHeader = "X-Aonohako-Pipeline-Observation-Budget-Ms"
	LegacyObservationBudgetHeader   = "X-Aonohako-Legacy-Observation-Budget-Ms"
)

type PipelineV1Capability struct {
	Versions               []int            `json:"versions"`
	MaxSteps               int              `json:"max_steps"`
	Executors              []string         `json:"executors"`
	ArtifactSources        []string         `json:"artifact_sources"`
	ArtifactSourceMaxBytes map[string]int64 `json:"artifact_source_max_bytes"`
	FinalJudges            []string         `json:"final_judges"`
}

type PipelineV1WorkerCapability struct {
	Protocol string                `json:"protocol"`
	Pipeline *PipelineV1Capability `json:"pipeline"`
}
