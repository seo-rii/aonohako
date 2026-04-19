package security

import (
	"path/filepath"
	"testing"
)

func TestWorkspaceScopedEnvIncludesWritableToolchainHomes(t *testing.T) {
	root := "/tmp/aonohako-work"
	env := WorkspaceScopedEnv(root)

	wants := []string{
		"HOME=" + filepath.Join(root, ".home"),
		"TMPDIR=" + filepath.Join(root, ".tmp"),
		"XDG_CACHE_HOME=" + filepath.Join(root, ".cache"),
		"MPLCONFIGDIR=" + filepath.Join(root, ".mpl"),
		"PIP_CACHE_DIR=" + filepath.Join(root, ".pip-cache"),
		"DOTNET_CLI_HOME=" + filepath.Join(root, ".dotnet-home"),
		"NUGET_PACKAGES=" + filepath.Join(root, ".nuget"),
		"KONAN_USER_HOME=" + filepath.Join(root, ".konan-home"),
		"KONAN_DATA_DIR=" + filepath.Join(root, ".konan"),
		"MIX_HOME=" + filepath.Join(root, ".mix"),
		"HEX_HOME=" + filepath.Join(root, ".hex"),
	}

	for _, want := range wants {
		found := false
		for _, got := range env {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("WorkspaceScopedEnv missing %q in %v", want, env)
		}
	}
}
