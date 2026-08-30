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
	} else if job.Profile.RunLang == "zsh" {
		checker.exts = []string{".zsh"}
		checker.bin = "/usr/bin/zsh"
		checker.prefix = []string{"-d", "-f", "-n"}
	} else if job.Profile.RunLang == "fish" {
		checker.exts = []string{".fish"}
		checker.bin = "/usr/bin/fish"
		checker.prefix = []string{"--no-config", "--private", "--no-execute"}
	}
	return checker.Compile(ctx, job)
}
