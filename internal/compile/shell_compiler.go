package compile

import (
	"context"

	"aonohako/internal/model"
)

type shellCompiler struct{}

func (shellCompiler) Compile(ctx context.Context, job CompileJob) model.CompileResponse {
	checker := scriptCheckCompiler{
		exts:           []string{".sh"},
		noSourceReason: "no shell sources",
		bin:            "bash",
		prefix:         []string{"--noprofile", "--norc", "-n"},
	}
	if job.Profile.RunLang == "posix-sh" {
		checker.bin = "/bin/dash"
		checker.prefix = []string{"-n"}
	}
	return checker.Compile(ctx, job)
}
