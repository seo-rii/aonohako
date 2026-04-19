package sandbox

import "aonohako/internal/model"

const (
	HelperModeEnv  = "AONOHAKO_INTERNAL_MODE"
	HelperModeExec = "sandbox-exec"
	RequestPathEnv = "AONOHAKO_SANDBOX_REQUEST"
)

type ExecRequest struct {
	Command       []string     `json:"command"`
	Dir           string       `json:"dir"`
	Env           []string     `json:"env"`
	Limits        model.Limits `json:"limits"`
	EnableNetwork bool         `json:"enable_network"`
}
