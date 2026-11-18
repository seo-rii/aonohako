package api

import (
	"fmt"

	"aonohako/internal/config"
	"aonohako/internal/model"
)

// ApplyRunRuntimePolicies applies the same runtime-profile and Python-library
// policies as /execute. Operator inspection uses this before describing effective
// options. It starts no server or runner and does not validate uploaded artifacts.
func ApplyRunRuntimePolicies(req *model.RunRequest, cfg config.Config) error {
	if req == nil {
		return fmt.Errorf("request is required")
	}
	policy := Server{cfg: cfg}
	if err := policy.applyRuntimeProfilePolicy(req.ProblemID, &req.RuntimeProfile); err != nil {
		return err
	}
	return policy.applyPythonLibraryPolicy(req)
}
