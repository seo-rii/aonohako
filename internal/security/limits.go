package security

import "path/filepath"

const dotnetFileSizeLimitBytes = 512 << 20

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
	switch filepath.Base(command) {
	case "dotnet":
		if workspaceBytes > dotnetFileSizeLimitBytes {
			return uint64(workspaceBytes)
		}
		return dotnetFileSizeLimitBytes
	default:
		return 0
	}
}
