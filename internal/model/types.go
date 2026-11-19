package model

import (
	"aonohako/internal/cpupolicy"
	"aonohako/internal/durante"
	"aonohako/internal/gomodulepolicy"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/rustpolicy"
)

type Source struct {
	Name    string `json:"name"`
	DataB64 string `json:"data_b64"`
	DataURL string `json:"data_url,omitempty"`
}

type Artifact struct {
	Name    string `json:"name"`
	DataB64 string `json:"data_b64"`
	Mode    string `json:"mode,omitempty"`
}

type CompileRequest struct {
	Lang                  string               `json:"lang"`
	Version               string               `json:"version,omitempty"`
	Sources               []Source             `json:"sources"`
	Target                string               `json:"target,omitempty"`
	EntryPoint            string               `json:"entry_point,omitempty"`
	ProblemID             string               `json:"problem_id,omitempty"`
	RuntimeProfile        string               `json:"runtime_profile,omitempty"`
	GoModuleMode          gomodulepolicy.Mode  `json:"go_module_mode,omitempty"`
	RustCrateMode         rustpolicy.CrateMode `json:"rust_crate_mode,omitempty"`
	DisallowedCPUFeatures []cpupolicy.Feature  `json:"disallowed_cpu_features,omitempty"`
	EmitLogs              *bool                `json:"emit_logs,omitempty"`
}

type CompileResponse struct {
	Status          string     `json:"status"`
	Artifacts       []Artifact `json:"artifacts,omitempty"`
	Stdout          string     `json:"stdout,omitempty"`
	Stderr          string     `json:"stderr,omitempty"`
	StdoutTruncated bool       `json:"stdout_truncated,omitempty"`
	StderrTruncated bool       `json:"stderr_truncated,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	ReasonTruncated bool       `json:"reason_truncated,omitempty"`
	ReasonCode      string     `json:"reason_code,omitempty"`
}

type Binary struct {
	Name    string `json:"name"`
	DataB64 string `json:"data_b64"`
	DataURL string `json:"data_url,omitempty"`
	Mode    string `json:"mode,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

type Limits struct {
	TimeMs         int   `json:"time_ms"`
	WallTimeMs     int   `json:"wall_time_ms,omitempty"`
	MemoryMB       int   `json:"memory_mb"`
	OutputBytes    int   `json:"output_bytes,omitempty"`
	WorkspaceBytes int64 `json:"workspace_bytes,omitempty"`
}

type CaptureLimits struct {
	StdoutBytes *int `json:"stdout_bytes,omitempty"`
	StderrBytes *int `json:"stderr_bytes,omitempty"`
}

type SPJSpec struct {
	Binary         *Binary      `json:"binary,omitempty"`
	Lang           string       `json:"lang,omitempty"`
	EmitScore      bool         `json:"emit_score,omitempty"`
	Limits         *Limits      `json:"limits,omitempty"`
	SidecarOutputs []OutputFile `json:"sidecar_outputs,omitempty"`
}

type InteractorSpec struct {
	Lang       string   `json:"lang"`
	Binaries   []Binary `json:"binaries"`
	EntryPoint string   `json:"entry_point,omitempty"`
	Limits     *Limits  `json:"limits,omitempty"`
}

type CommunicationSpec struct {
	Version               int      `json:"version"`
	ParticipantProgramID  string   `json:"participant_program_id"`
	ParticipantProgramIDs []string `json:"participant_program_ids,omitempty"`
	ManagerProgramID      string   `json:"manager_program_id"`
	ParticipantCount      int      `json:"participant_count"`
	ResultProtocol        string   `json:"result_protocol"`
	Input                 string   `json:"input,omitempty"`
	InputURL              string   `json:"input_url,omitempty"`
	InputSHA256           string   `json:"input_sha256,omitempty"`
	RuntimeFingerprint    string   `json:"runtime_fingerprint,omitempty"`
	Answer                string   `json:"answer,omitempty"`
	AnswerURL             string   `json:"answer_url,omitempty"`
	MatchSeed             string   `json:"match_seed,omitempty"`
}

type OutputFile struct {
	Path string `json:"path"`
}

type RunProgram struct {
	ID            string   `json:"id"`
	Lang          string   `json:"lang"`
	Binaries      []Binary `json:"binaries"`
	EntryPoint    string   `json:"entry_point,omitempty"`
	EnableNetwork bool     `json:"enable_network,omitempty"`
	SourceSHA256  string   `json:"source_sha256,omitempty"`
}

type MatchArtifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type MatchParticipant struct {
	Seat          int             `json:"seat"`
	ProgramID     string          `json:"program_id"`
	SourceSHA256  string          `json:"source_sha256"`
	EntryPoint    string          `json:"entry_point,omitempty"`
	Artifacts     []MatchArtifact `json:"artifacts"`
	Status        string          `json:"status"`
	Reason        string          `json:"reason,omitempty"`
	VerdictSource string          `json:"verdict_source,omitempty"`
	CPUTimeMs     int64           `json:"cpu_time_ms"`
	MemoryKB      int64           `json:"memory_kb"`
}

// CommunicationRoleCPU identifies the target whose process tree the native
// sandbox timed, including repeated seats running the same program.
type CommunicationRoleCPU struct {
	Role             string  `json:"role"`
	ProgramID        string  `json:"program_id"`
	ParticipantIndex *int    `json:"participant_index,omitempty"`
	Started          bool    `json:"started"`
	Interrupted      bool    `json:"interrupted"`
	ExecutionStatus  string  `json:"execution_status"`
	ExitCode         *int    `json:"exit_code,omitempty"`
	CPULimitMs       int     `json:"cpu_limit_ms"`
	CPUTimeMs        int64   `json:"cpu_time_ms"`
	CPUTimeNs        *uint64 `json:"cpu_time_ns,omitempty"`
	RawCPUTimeMs     *int64  `json:"raw_cpu_time_ms,omitempty"`
	RawCPUTimeNs     *uint64 `json:"raw_cpu_time_ns,omitempty"`
}

type MatchResult struct {
	Version            int                `json:"version"`
	Outcome            string             `json:"outcome"`
	Games              int                `json:"games"`
	Wins               *[2]int            `json:"wins,omitempty"`
	Retriable          bool               `json:"retriable"`
	Participants       []MatchParticipant `json:"participants"`
	Message            string             `json:"message,omitempty"`
	MatchSeed          string             `json:"match_seed,omitempty"`
	InputSHA256        string             `json:"input_sha256,omitempty"`
	RuntimeFingerprint string             `json:"runtime_fingerprint,omitempty"`
	RunnerSHA256       string             `json:"runner_sha256,omitempty"`
	ImageDigest        string             `json:"image_digest,omitempty"`
	ErrorCode          string             `json:"error_code,omitempty"`
}

type StepHandoff struct {
	ID       string `json:"id"`
	From     string `json:"from,omitempty"`
	Path     string `json:"path,omitempty"`
	MaxBytes int64  `json:"max_bytes,omitempty"`
}

type StdinPart struct {
	Type    string `json:"type"`
	Data    string `json:"data,omitempty"`
	DataURL string `json:"data_url,omitempty"`
	ID      string `json:"id,omitempty"`
	From    string `json:"from,omitempty"`
}

type RunStep struct {
	ID         string       `json:"id"`
	ProgramID  string       `json:"program_id"`
	Stdin      string       `json:"stdin,omitempty"`
	StdinURL   string       `json:"stdin_url,omitempty"`
	StdinFrom  string       `json:"stdin_from,omitempty"`
	StdinParts []StdinPart  `json:"stdin_parts,omitempty"`
	Limits     Limits       `json:"limits"`
	Handoff    *StepHandoff `json:"handoff,omitempty"`
}

// PipelineResource is immutable request-owned data. The API resolves DataURL
// once before execution and the runner keeps the resulting Data private for
// the lifetime of the pipeline.
type PipelineResource struct {
	DataB64 string `json:"data_b64,omitempty"`
	DataURL string `json:"data_url,omitempty"`
}

type PipelineRef struct {
	Type   string `json:"type"`
	ID     string `json:"id,omitempty"`
	StepID string `json:"step_id,omitempty"`
}

type PipelineExecutor struct {
	Kind                 string       `json:"kind"`
	ProgramID            string       `json:"program_id,omitempty"`
	ParticipantProgramID string       `json:"participant_program_id,omitempty"`
	InteractorProgramID  string       `json:"interactor_program_id,omitempty"`
	InteractorLimits     *Limits      `json:"interactor_limits,omitempty"`
	InteractorAnswer     *PipelineRef `json:"interactor_answer,omitempty"`
}

type PipelineOutputSource struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
}

type PipelineOutput struct {
	ID       string               `json:"id"`
	Source   PipelineOutputSource `json:"source"`
	MaxBytes int64                `json:"max_bytes"`
}

type PipelineStep struct {
	ID       string           `json:"id"`
	Executor PipelineExecutor `json:"executor"`
	Stdin    []PipelineRef    `json:"stdin,omitempty"`
	Outputs  []PipelineOutput `json:"outputs,omitempty"`
	Limits   Limits           `json:"limits"`
}

type PipelineFinalJudge struct {
	Kind     string      `json:"kind"`
	Input    PipelineRef `json:"input"`
	Expected PipelineRef `json:"expected"`
	Actual   PipelineRef `json:"actual"`
	SPJ      *SPJSpec    `json:"spj,omitempty"`
}

type PipelineV1 struct {
	Version    int                         `json:"version"`
	Resources  map[string]PipelineResource `json:"resources"`
	Programs   []RunProgram                `json:"programs"`
	Steps      []PipelineStep              `json:"steps"`
	FinalJudge PipelineFinalJudge          `json:"final_judge"`
}

// PipelineRoleCPU exposes native role accounting without peer output or diagnostics.
type PipelineRoleCPU struct {
	ProgramID    string  `json:"program_id"`
	Status       string  `json:"status"`
	ExitCode     *int    `json:"exit_code,omitempty"`
	CPUTimeMs    int64   `json:"cpu_time_ms"`
	CPUTimeNs    *uint64 `json:"cpu_time_ns,omitempty"`
	RawCPUTimeNs *uint64 `json:"raw_cpu_time_ns,omitempty"`
}

type PipelineRoleTimes struct {
	Contestant PipelineRoleCPU `json:"contestant"`
	Interactor PipelineRoleCPU `json:"interactor"`
}

type StepResult struct {
	CPUTimeByRole    *PipelineRoleTimes `json:"cpu_time_by_role,omitempty"`
	ID               string             `json:"id"`
	ProgramID        string             `json:"program_id,omitempty"`
	Status           string             `json:"status"`
	TimeMs           int64              `json:"time_ms"`
	WallTimeMs       int64              `json:"wall_time_ms"`
	CPUTimeMs        int64              `json:"cpu_time_ms"`
	CPUTimeNs        *uint64            `json:"cpu_time_ns,omitempty"`
	RawCPUTimeNs     *uint64            `json:"raw_cpu_time_ns,omitempty"`
	RawCPUTimeMs     *int64             `json:"raw_cpu_time_ms,omitempty"`
	ProcessCPUTimeMs int64              `json:"process_cpu_time_ms,omitempty"`
	MemoryKB         int64              `json:"memory_kb"`
	ExitCode         *int               `json:"exit_code,omitempty"`
	Stdout           string             `json:"stdout,omitempty"`
	Stderr           string             `json:"stderr,omitempty"`
	StdoutTruncated  bool               `json:"stdout_truncated,omitempty"`
	StderrTruncated  bool               `json:"stderr_truncated,omitempty"`
	Reason           string             `json:"reason,omitempty"`
	ReasonTruncated  bool               `json:"reason_truncated,omitempty"`
	VerdictSource    string             `json:"verdict_source,omitempty"`
	HandoffBytes     int64              `json:"handoff_bytes,omitempty"`
}

type CPUTimeNormalization struct {
	Method          string `json:"method"`
	ScalePPM        int64  `json:"scale_ppm"`
	ReferenceTimeNs uint64 `json:"reference_time_ns"`
	ObservedTimeNs  uint64 `json:"observed_time_ns"`
}

// CPUAccounting keeps the independent raw accounting inputs auditable. These
// values describe only resource counters, never contestant input or output.
type CPUAccounting struct {
	ProcessClockBaselineNs uint64 `json:"process_clock_baseline_ns"`
	RusageBaselineNs       uint64 `json:"rusage_baseline_ns"`
	ProcessClockPeakNs     uint64 `json:"process_clock_peak_ns"`
	WaitUserNs             uint64 `json:"wait_user_ns"`
	WaitSystemNs           uint64 `json:"wait_system_ns"`
	WaitTargetNs           uint64 `json:"wait_target_ns"`
	SelectedSource         string `json:"selected_source"`
}

type CPUTimeSample struct {
	Status        string         `json:"status"`
	CPUTimeMs     int64          `json:"cpu_time_ms"`
	CPUTimeNs     uint64         `json:"cpu_time_ns"`
	RawCPUTimeMs  *int64         `json:"raw_cpu_time_ms,omitempty"`
	RawCPUTimeNs  *uint64        `json:"raw_cpu_time_ns,omitempty"`
	WallTimeMs    int64          `json:"wall_time_ms"`
	MemoryKB      int64          `json:"memory_kb"`
	CPUAccounting *CPUAccounting `json:"cpu_accounting,omitempty"`
}

type CPUTimeSampling struct {
	Method         string          `json:"method"`
	SelectedSample int             `json:"selected_sample"`
	Samples        []CPUTimeSample `json:"samples"`
}

type SidecarOutput struct {
	Path    string `json:"path"`
	DataB64 string `json:"data_b64"`
}

type SidecarError struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type RunRequest struct {
	WorkMeasurement         bool                      `json:"work_measurement,omitempty"`
	WorkMeasurementLimits   *durante.ResourceLimits   `json:"work_measurement_limits,omitempty"`
	WorkMeasurementClock    *durante.WorkClockRequest `json:"work_measurement_clock,omitempty"`
	WorkMeasurementTime     bool                      `json:"work_measurement_time,omitempty"`
	WorkMeasurementJudge    bool                      `json:"work_measurement_judge,omitempty"`
	JudgeClockBinding       string                    `json:"work_measurement_judge_clock_binding,omitempty"`
	WorkMeasurementShadow   bool                      `json:"work_measurement_shadow,omitempty"`
	WorkMeasurementPeerTime *PeerTimeRequest          `json:"work_measurement_peer_time,omitempty"`
	Lang                    string                    `json:"lang"`
	EntryMode               string                    `json:"entry_mode,omitempty"`
	Binaries                []Binary                  `json:"binaries"`
	Programs                []RunProgram              `json:"programs,omitempty"`
	Steps                   []RunStep                 `json:"steps,omitempty"`
	Stdin                   string                    `json:"stdin"`
	StdinURL                string                    `json:"stdin_url,omitempty"`
	ExpectedStdout          string                    `json:"expected_stdout,omitempty"`
	ExpectedStdoutURL       string                    `json:"expected_stdout_url,omitempty"`
	Limits                  Limits                    `json:"limits"`
	CaptureLimits           *CaptureLimits            `json:"capture_limits,omitempty"`
	ProblemID               string                    `json:"problem_id,omitempty"`
	RuntimeProfile          string                    `json:"runtime_profile,omitempty"`
	PythonLibraryMode       pythonpolicy.LibraryMode  `json:"python_library_mode,omitempty"`
	DisallowedCPUFeatures   []cpupolicy.Feature       `json:"disallowed_cpu_features,omitempty"`
	EnableNetwork           bool                      `json:"enable_network,omitempty"`
	EntryPoint              string                    `json:"entry_point,omitempty"`
	SPJ                     *SPJSpec                  `json:"spj,omitempty"`
	Interactor              *InteractorSpec           `json:"interactor,omitempty"`
	Communication           *CommunicationSpec        `json:"communication,omitempty"`
	FileOutputs             []OutputFile              `json:"file_outputs,omitempty"`
	SidecarOutputs          []OutputFile              `json:"sidecar_outputs,omitempty"`
	EmitLogs                *bool                     `json:"emit_logs,omitempty"`
	IgnoreTLE               bool                      `json:"ignore_tle,omitempty"`
	Pipeline                *PipelineV1               `json:"pipeline,omitempty"`
}

type RunResponse struct {
	WorkMeasurementObservation *WorkMeasurementObservation `json:"work_measurement_observation,omitempty"`

	WorkMeasurement          *durante.Report           `json:"work_measurement,omitempty"`
	WorkMeasurements         []WorkMeasurementProcess  `json:"work_measurements,omitempty"`
	WorkMeasurementExecution *WorkMeasurementExecution `json:"work_measurement_execution,omitempty"`
	WorkMeasurementJudgment  *WorkMeasurementJudgment  `json:"work_measurement_judgment,omitempty"`
	WorkMeasurementShadow    *WorkMeasurementShadow    `json:"work_measurement_shadow,omitempty"`
	Status                   string                    `json:"status"`
	TimeMs                   int64                     `json:"time_ms"`
	WallTimeMs               int64                     `json:"wall_time_ms"`
	CPUTimeMs                int64                     `json:"cpu_time_ms"`
	CPUTimeNs                uint64                    `json:"cpu_time_ns,omitempty"`
	RawCPUTimeMs             *int64                    `json:"raw_cpu_time_ms,omitempty"`
	RawCPUTimeNs             *uint64                   `json:"raw_cpu_time_ns,omitempty"`
	CPUAccounting            *CPUAccounting            `json:"cpu_accounting,omitempty"`
	CPUTimeSampling          *CPUTimeSampling          `json:"cpu_time_sampling,omitempty"`
	ProcessCPUTimeMs         int64                     `json:"process_cpu_time_ms,omitempty"`
	CPUTimeNormalization     *CPUTimeNormalization     `json:"cpu_time_normalization,omitempty"`
	MemoryKB                 int64                     `json:"memory_kb"`
	ExitCode                 *int                      `json:"exit_code,omitempty"`
	Stdout                   string                    `json:"stdout,omitempty"`
	Stderr                   string                    `json:"stderr,omitempty"`
	StdoutTruncated          bool                      `json:"stdout_truncated,omitempty"`
	StderrTruncated          bool                      `json:"stderr_truncated,omitempty"`
	Reason                   string                    `json:"reason,omitempty"`
	ReasonTruncated          bool                      `json:"reason_truncated,omitempty"`
	VerdictSource            string                    `json:"verdict_source,omitempty"`
	Score                    *float64                  `json:"score,omitempty"`
	CommunicationCPUByRole   []CommunicationRoleCPU    `json:"communication_cpu_by_role,omitempty"`
	StartedParticipants      int                       `json:"started_participants,omitempty"`
	Match                    *MatchResult              `json:"match,omitempty"`
	Steps                    []StepResult              `json:"steps,omitempty"`
	SidecarOutputs           []SidecarOutput           `json:"sidecar_outputs,omitempty"`
	SidecarErrors            []SidecarError            `json:"sidecar_errors,omitempty"`
}

// WorkMeasurementProcess keeps each concurrent role's diagnostic observation
// separate. ExecutionStatus and TerminationSource describe the actual execution
// before interactive or match coordinator verdicts reinterpret a peer shutdown.
type WorkMeasurementProcess struct {
	Role              string                `json:"role"`
	ProgramID         string                `json:"program_id"`
	ParticipantIndex  *int                  `json:"participant_index,omitempty"`
	ExecutionStatus   string                `json:"execution_status"`
	ExitCode          *int                  `json:"exit_code,omitempty"`
	TerminationSource string                `json:"termination_source,omitempty"`
	Report            *durante.Report       `json:"report,omitempty"`
	TimeBudget        *PeerTimeBudgetResult `json:"time_budget,omitempty"`
}

// PeerTimeRequest compares each role's completed diagnostic observation with an
// explicit budget. It does not replace runtime limits or the peer verdict.
type PeerTimeRequest struct {
	Version int              `json:"version"`
	Budgets []PeerTimeBudget `json:"budgets"`
}

type PeerTimeBudget struct {
	ModelBinding     string `json:"model_binding,omitempty"`
	Role             string `json:"role"`
	ProgramID        string `json:"program_id"`
	ParticipantIndex *int   `json:"participant_index,omitempty"`
	TimeMs           int    `json:"time_ms"`
}

type PeerClockUsage struct {
	StdoutLimitExceeded bool    `json:"stdout_limit_exceeded,omitempty"`
	CPUTimeNs           *uint64 `json:"cpu_time_ns"`
	WallTimeMs          *int64  `json:"wall_time_ms"`
	MemoryKB            *int64  `json:"memory_kb"`
}

type PeerTimeBudgetResult struct {
	Clock       *durante.WorkClockReceipt `json:"clock,omitempty"`
	RawLimits   *durante.ResourceLimits   `json:"raw_limits,omitempty"`
	RawUsage    *PeerClockUsage           `json:"raw_usage,omitempty"`
	RawSource   string                    `json:"raw_source,omitempty"`
	Version     int                       `json:"version"`
	Authority   string                    `json:"authority"`
	BudgetMs    int                       `json:"budget_ms"`
	Estimate    *durante.TimeEstimate     `json:"estimate,omitempty"`
	Exceeded    *bool                     `json:"exceeded,omitempty"`
	Unavailable string                    `json:"unavailable,omitempty"`
}

// WorkMeasurementExecution describes an opt-in diagnostic execution. Its
// resource envelope is not a contestant's credited time or memory allowance.
// The enclosing response is always InitFail until an authoritative judgement
// policy exists, including when this diagnostic completed successfully.
type WorkMeasurementExecution struct {
	Authority               string                    `json:"authority"`
	State                   string                    `json:"state"`
	NominalLimits           Limits                    `json:"nominal_limits"`
	RawLimits               durante.ResourceLimits    `json:"raw_limits"`
	OutputMatches           *bool                     `json:"output_matches,omitempty"`
	Clock                   *durante.WorkClockReceipt `json:"clock,omitempty"`
	TerminationSource       string                    `json:"termination_source,omitempty"`
	TimeEstimate            *durante.TimeEstimate     `json:"time_estimate,omitempty"`
	NominalTimeExceeded     *bool                     `json:"nominal_time_exceeded,omitempty"`
	TimeEstimateUnavailable string                    `json:"time_estimate_unavailable,omitempty"`
	TimeEstimateEligibility *durante.TimeEligibility  `json:"time_estimate_eligibility,omitempty"`
}

// PipelineStepObservation identifies the native stage or contestant supplying replay evidence.
type PipelineStepObservation struct {
	StepID      string          `json:"step_id"`
	ProgramID   string          `json:"program_id"`
	State       string          `json:"state"`
	Reason      string          `json:"reason,omitempty"`
	OutputCheck string          `json:"output_check,omitempty"`
	Report      *durante.Report `json:"report,omitempty"`
}

// WorkMeasurementObservation describes a batch replay (v1), or identifies the
// contestant in standalone interactive, legacy step and Pipeline V1 replays (v2).
// The enclosing response carries the native verdict.
type WorkMeasurementObservation struct {
	Steps       []PipelineStepObservation `json:"steps,omitempty"`
	OutputCheck string                    `json:"output_check,omitempty"`
	Version     int                       `json:"version"`
	Authority   string                    `json:"authority"`
	State       string                    `json:"state"`
	Reason      string                    `json:"reason,omitempty"`
	StdinSHA256 string                    `json:"stdin_sha256,omitempty"`
	Report      *durante.Report           `json:"report,omitempty"`
}

// WorkMeasurementShadow compares the authoritative verdict of this response
// with a separate judge-mode execution of the same request. It never changes
// the enclosing Status, Score or resource values; see docs/durante-judging.md.
// Response is the trimmed shadow execution (no stdout/stderr/sidecars), kept so
// a control plane can recompute the shadow verdict from its raw observation.
type WorkMeasurementShadow struct {
	Version          int          `json:"version"`
	Authority        string       `json:"authority"`
	Agreement        string       `json:"agreement"`
	PrimaryStatus    string       `json:"primary_status"`
	PrimaryCPUTimeMs int64        `json:"primary_cpu_time_ms"`
	ShadowStatus     string       `json:"shadow_status,omitempty"`
	StdinSHA256      string       `json:"stdin_sha256,omitempty"` // digest of the shared URL input file
	Unavailable      string       `json:"unavailable,omitempty"`
	Response         *RunResponse `json:"response,omitempty"`
}

// Shadow agreement values.
const (
	ShadowAgree       = "agree"
	ShadowDisagree    = "disagree"
	ShadowUnavailable = "unavailable"
)

// WorkMeasurementJudgment is judge mode's verdict over a diagnostic execution.
// Unlike the pure envelope, judge mode sets the response Status to this Verdict.
// The counts remain diagnostic_only and application-mutable; the verdict is an
// operator policy over them, not a trusted measurement. See docs/durante-judging.md.
type WorkMeasurementJudgment struct {
	Authority         string `json:"authority"`
	Verdict           string `json:"verdict"`
	Reason            string `json:"reason,omitempty"`
	State             string `json:"state"`
	NominalTimeMs     int64  `json:"nominal_time_ms"`
	EstimatedTimeMs   *int64 `json:"estimated_time_ms,omitempty"`
	EstimatedTimeNs   *int64 `json:"estimated_time_ns,omitempty"`
	IntegrityMode     string `json:"integrity_mode"`
	IntegrityFlagged  bool   `json:"integrity_flagged,omitempty"`
	TerminationSource string `json:"termination_source,omitempty"`
}

const (
	CompileStatusOK           = "OK"
	CompileStatusCompileError = "Compile Error"
	CompileStatusTimeout      = "Timeout"
	CompileStatusInvalid      = "Invalid Request"
	CompileStatusInternal     = "Internal Error"
)

const (
	RunStatusAccepted = "Accepted"
	RunStatusWA       = "Wrong Answer"
	RunStatusTLE      = "Time Limit Exceeded"
	RunStatusMLE      = "Memory Limit Exceeded"
	RunStatusWLE      = "Workspace Limit Exceeded"
	RunStatusRE       = "Runtime Error"
	RunStatusInitFail = "Container Initialization Failed"
)
