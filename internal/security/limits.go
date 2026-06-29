package security

import "path/filepath"

const DotnetFileSizeLimitBytes uint64 = 2 << 40

func OpenFileLimitForCommand(command string) int {
	switch filepath.Base(command) {
	case "aonohako-acl2-check", "aonohako-alloy-check", "aonohako-kframework-check", "aonohako-tla-run", "dafny", "dotnet", "fstar.exe", "isabelle", "kompile":
		return 512
	case "R", "Rscript":
		return 256
	default:
		return 64
	}
}

func FileSizeLimitForCommand(command string, workspaceBytes int64) uint64 {
	switch filepath.Base(command) {
	case "dafny", "dotnet":
		if workspaceBytes > 0 && uint64(workspaceBytes) > DotnetFileSizeLimitBytes {
			return uint64(workspaceBytes)
		}
		return DotnetFileSizeLimitBytes
	default:
		return 0
	}
}

func StackLimitForCommand(command string) uint64 {
	return 0
}
