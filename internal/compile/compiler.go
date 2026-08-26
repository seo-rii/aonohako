package compile

import (
	"context"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/rustpolicy"
)

type Compiler interface {
	Compile(context.Context, CompileJob) model.CompileResponse
}

type CompileJob struct {
	WorkDir string
	Target  string
	Profile profiles.Profile
	Request *model.CompileRequest
	Tuning  config.RuntimeTuningConfig
	Runner  CommandRunner
}

type CommandRunner interface {
	Run(ctx context.Context, workDir, bin string, args, env []string) CommandResult
}

type CommandResult struct {
	Stdout string
	Stderr string
	Status string
	Reason string
}

type sandboxCommandRunner struct {
	supplementaryGroups []uint32
	workspaceDirs       []string
}

func (r sandboxCommandRunner) Run(ctx context.Context, workDir, bin string, args, env []string) CommandResult {
	stdout, stderr, status, reason := runSandboxedCommandWithGroups(ctx, workDir, bin, args, env, r.supplementaryGroups, r.workspaceDirs)
	return CommandResult{Stdout: stdout, Stderr: stderr, Status: status, Reason: reason}
}

func sandboxCommandRunnerForRustMode(mode rustpolicy.CrateMode) sandboxCommandRunner {
	runner := sandboxCommandRunner{}
	if rustpolicy.EffectiveCrateMode(mode) == rustpolicy.CrateModeInstalled {
		runner.supplementaryGroups = []uint32{rustpolicy.ExternalCrateGID}
		runner.workspaceDirs = []string{".cargo-home", ".cargo-target"}
	}
	return runner
}
