package compile

import (
	"context"

	"aonohako/internal/config"
	"aonohako/internal/gomodulepolicy"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
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
}

func (r sandboxCommandRunner) Run(ctx context.Context, workDir, bin string, args, env []string) CommandResult {
	stdout, stderr, status, reason := runSandboxedCommandWithGroups(ctx, workDir, bin, args, env, r.supplementaryGroups)
	return CommandResult{Stdout: stdout, Stderr: stderr, Status: status, Reason: reason}
}

func sandboxCommandRunnerForGoMode(mode gomodulepolicy.Mode) sandboxCommandRunner {
	runner := sandboxCommandRunner{}
	if gomodulepolicy.EffectiveMode(mode) == gomodulepolicy.ModeInstalled {
		runner.supplementaryGroups = []uint32{gomodulepolicy.ExternalModuleGID}
	}
	return runner
}
