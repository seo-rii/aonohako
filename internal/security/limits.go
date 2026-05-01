package security

import "path/filepath"

func OpenFileLimitForCommand(command string) int {
	switch filepath.Base(command) {
	case "dotnet":
		return 512
	case "R", "Rscript":
		return 256
	default:
		return 64
	}
}

func FileSizeLimitForCommand(command string, workspaceBytes int64) uint64 {
	return 0
}

func StackLimitForCommand(command string) uint64 {
	switch filepath.Base(command) {
	case "dotnet":
		return 64 * 1024 * 1024
	default:
		return 8 * 1024 * 1024
	}
}
