package pythonpolicy

import (
	"fmt"
	"strings"
)

type ArtifactMode string

// ArtifactModeBytecodeV1 returns source-free bytecode with canonical headers.
// The default empty mode preserves source files and checked-hash bytecode.
const ArtifactModeBytecodeV1 ArtifactMode = "bytecode-v1"

func ValidateArtifactMode(mode ArtifactMode, language string) error {
	if mode == "" {
		return nil
	}
	if mode != ArtifactModeBytecodeV1 {
		return fmt.Errorf("python_artifact_mode must be omitted or %q", ArtifactModeBytecodeV1)
	}
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "python", "python3", "pypy", "pypy3":
		return nil
	default:
		return fmt.Errorf("python_artifact_mode requires a Python or PyPy compile language")
	}
}
