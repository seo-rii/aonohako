package sandbox

import "aonohako/internal/model"

const (
	HelperModeEnv  = "AONOHAKO_INTERNAL_MODE"
	HelperModeExec = "sandbox-exec"
	RequestPathEnv = "AONOHAKO_SANDBOX_REQUEST"
	RequestFDEnv   = "AONOHAKO_SANDBOX_REQUEST_FD"
)

type ExecRequest struct {
	Command                  []string     `json:"command"`
	Dir                      string       `json:"dir"`
	Env                      []string     `json:"env"`
	Limits                   model.Limits `json:"limits"`
	ThreadLimit              int          `json:"thread_limit"`
	OpenFileLimit            int          `json:"open_file_limit,omitempty"`
	StackLimitBytes          uint64       `json:"stack_limit_bytes,omitempty"`
	AddressSpaceLimitBytes   uint64       `json:"address_space_limit_bytes,omitempty"`
	FileSizeLimitBytes       uint64       `json:"file_size_limit_bytes,omitempty"`
	EnableNetwork            bool         `json:"enable_network"`
	AllowUnixSockets         bool         `json:"allow_unix_sockets,omitempty"`
	AllowUnixSocketMessages  bool         `json:"allow_unix_socket_messages,omitempty"`
	AllowProcesses           bool         `json:"allow_processes,omitempty"`
	AllowProcessGroups       bool         `json:"allow_process_groups,omitempty"`
	AllowMemfdCreate         bool         `json:"allow_memfd_create,omitempty"`
	AllowNumaPolicy          bool         `json:"allow_numa_policy,omitempty"`
	AllowChmod               bool         `json:"allow_chmod,omitempty"`
	DisableFileSizeLimit     bool         `json:"disable_file_size_limit,omitempty"`
	DisableAddressSpaceLimit bool         `json:"disable_address_space_limit,omitempty"`
}
