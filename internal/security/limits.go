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
