package model

type Source struct {
	Name    string `json:"name"`
	DataB64 string `json:"data_b64"`
}

type Artifact struct {
	Name    string `json:"name"`
	DataB64 string `json:"data_b64"`
	Mode    string `json:"mode,omitempty"`
}

type CompileRequest struct {
	Lang       string   `json:"lang"`
	Sources    []Source `json:"sources"`
	Target     string   `json:"target,omitempty"`
	EntryPoint string   `json:"entry_point,omitempty"`
}

type CompileResponse struct {
	Status    string     `json:"status"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
	Stdout    string     `json:"stdout,omitempty"`
	Stderr    string     `json:"stderr,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

type Binary struct {
	Name    string `json:"name"`
	DataB64 string `json:"data_b64"`
	Mode    string `json:"mode,omitempty"`
}

type Limits struct {
	TimeMs   int `json:"time_ms"`
	MemoryMB int `json:"memory_mb"`
}

type SPJSpec struct {
	Binary    *Binary `json:"binary,omitempty"`
	Lang      string  `json:"lang,omitempty"`
	EmitScore bool    `json:"emit_score,omitempty"`
}

type OutputFile struct {
	Path string `json:"path"`
}

type SidecarOutput struct {
	Path    string `json:"path"`
	DataB64 string `json:"data_b64"`
}

type RunRequest struct {
	Lang           string       `json:"lang"`
	Binaries       []Binary     `json:"binaries"`
	Stdin          string       `json:"stdin"`
	ExpectedStdout string       `json:"expected_stdout,omitempty"`
	Limits         Limits       `json:"limits"`
	EnableNetwork  bool         `json:"enable_network,omitempty"`
	EntryPoint     string       `json:"entry_point,omitempty"`
	SPJ            *SPJSpec     `json:"spj,omitempty"`
	FileOutputs    []OutputFile `json:"file_outputs,omitempty"`
	SidecarOutputs []OutputFile `json:"sidecar_outputs,omitempty"`
	IgnoreTLE      bool         `json:"ignore_tle,omitempty"`
}

type RunResponse struct {
	Status         string          `json:"status"`
	TimeMs         int64           `json:"time_ms"`
	MemoryKB       int64           `json:"memory_kb"`
	ExitCode       *int            `json:"exit_code,omitempty"`
	Stdout         string          `json:"stdout,omitempty"`
	Stderr         string          `json:"stderr,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Score          *float64        `json:"score,omitempty"`
	SidecarOutputs []SidecarOutput `json:"sidecar_outputs,omitempty"`
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
	RunStatusRE       = "Runtime Error"
	RunStatusInitFail = "Container Initialization Failed"
)
