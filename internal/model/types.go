package model

import (
	"aonohako/internal/gomodulepolicy"
	"aonohako/internal/pythonpolicy"
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
	Lang           string              `json:"lang"`
	Version        string              `json:"version,omitempty"`
	Sources        []Source            `json:"sources"`
	Target         string              `json:"target,omitempty"`
	EntryPoint     string              `json:"entry_point,omitempty"`
	ProblemID      string              `json:"problem_id,omitempty"`
	RuntimeProfile string              `json:"runtime_profile,omitempty"`
	GoModuleMode   gomodulepolicy.Mode `json:"go_module_mode,omitempty"`
	EmitLogs       *bool               `json:"emit_logs,omitempty"`
}

type CompileResponse struct {
	Status          string     `json:"status"`
	Artifacts       []Artifact `json:"artifacts,omitempty"`
	Stdout          string     `json:"stdout,omitempty"`
	Stderr          string     `json:"stderr,omitempty"`
	StdoutTruncated bool       `json:"stdout_truncated,omitempty"`
	StderrTruncated bool       `json:"stderr_truncated,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	ReasonCode      string     `json:"reason_code,omitempty"`
}

type Binary struct {
	Name    string `json:"name"`
	DataB64 string `json:"data_b64"`
	DataURL string `json:"data_url,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

type Limits struct {
	TimeMs         int   `json:"time_ms"`
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
	Version              int    `json:"version"`
	ParticipantProgramID string `json:"participant_program_id"`
	ManagerProgramID     string `json:"manager_program_id"`
	ParticipantCount     int    `json:"participant_count"`
	ResultProtocol       string `json:"result_protocol"`
	Input                string `json:"input,omitempty"`
	InputURL             string `json:"input_url,omitempty"`
	Answer               string `json:"answer,omitempty"`
	AnswerURL            string `json:"answer_url,omitempty"`
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

type StepResult struct {
	ID               string `json:"id"`
	ProgramID        string `json:"program_id,omitempty"`
	Status           string `json:"status"`
	TimeMs           int64  `json:"time_ms"`
	WallTimeMs       int64  `json:"wall_time_ms"`
	CPUTimeMs        int64  `json:"cpu_time_ms"`
	ProcessCPUTimeMs int64  `json:"process_cpu_time_ms,omitempty"`
	MemoryKB         int64  `json:"memory_kb"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Stdout           string `json:"stdout,omitempty"`
	Stderr           string `json:"stderr,omitempty"`
	StdoutTruncated  bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated  bool   `json:"stderr_truncated,omitempty"`
	Reason           string `json:"reason,omitempty"`
	VerdictSource    string `json:"verdict_source,omitempty"`
	HandoffBytes     int64  `json:"handoff_bytes,omitempty"`
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
	Lang              string                   `json:"lang"`
	Binaries          []Binary                 `json:"binaries"`
	Programs          []RunProgram             `json:"programs,omitempty"`
	Steps             []RunStep                `json:"steps,omitempty"`
	Stdin             string                   `json:"stdin"`
	StdinURL          string                   `json:"stdin_url,omitempty"`
	ExpectedStdout    string                   `json:"expected_stdout,omitempty"`
	ExpectedStdoutURL string                   `json:"expected_stdout_url,omitempty"`
	Limits            Limits                   `json:"limits"`
	CaptureLimits     *CaptureLimits           `json:"capture_limits,omitempty"`
	ProblemID         string                   `json:"problem_id,omitempty"`
	RuntimeProfile    string                   `json:"runtime_profile,omitempty"`
	PythonLibraryMode pythonpolicy.LibraryMode `json:"python_library_mode,omitempty"`
	EnableNetwork     bool                     `json:"enable_network,omitempty"`
	EntryPoint        string                   `json:"entry_point,omitempty"`
	SPJ               *SPJSpec                 `json:"spj,omitempty"`
	Interactor        *InteractorSpec          `json:"interactor,omitempty"`
	Communication     *CommunicationSpec       `json:"communication,omitempty"`
	FileOutputs       []OutputFile             `json:"file_outputs,omitempty"`
	SidecarOutputs    []OutputFile             `json:"sidecar_outputs,omitempty"`
	EmitLogs          *bool                    `json:"emit_logs,omitempty"`
	IgnoreTLE         bool                     `json:"ignore_tle,omitempty"`
}

type RunResponse struct {
	Status              string          `json:"status"`
	TimeMs              int64           `json:"time_ms"`
	WallTimeMs          int64           `json:"wall_time_ms"`
	CPUTimeMs           int64           `json:"cpu_time_ms"`
	ProcessCPUTimeMs    int64           `json:"process_cpu_time_ms,omitempty"`
	MemoryKB            int64           `json:"memory_kb"`
	ExitCode            *int            `json:"exit_code,omitempty"`
	Stdout              string          `json:"stdout,omitempty"`
	Stderr              string          `json:"stderr,omitempty"`
	StdoutTruncated     bool            `json:"stdout_truncated,omitempty"`
	StderrTruncated     bool            `json:"stderr_truncated,omitempty"`
	Reason              string          `json:"reason,omitempty"`
	VerdictSource       string          `json:"verdict_source,omitempty"`
	Score               *float64        `json:"score,omitempty"`
	StartedParticipants int             `json:"started_participants,omitempty"`
	Steps               []StepResult    `json:"steps,omitempty"`
	SidecarOutputs      []SidecarOutput `json:"sidecar_outputs,omitempty"`
	SidecarErrors       []SidecarError  `json:"sidecar_errors,omitempty"`
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
