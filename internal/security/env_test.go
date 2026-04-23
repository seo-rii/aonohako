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
		"TMP=" + filepath.Join(root, ".tmp"),
		"TEMP=" + filepath.Join(root, ".tmp"),
		"TEMPDIR=" + filepath.Join(root, ".tmp"),
		"JAVA_TOOL_OPTIONS=-Djava.io.tmpdir=" + filepath.Join(root, ".tmp"),
		"XDG_CACHE_HOME=" + filepath.Join(root, ".cache"),
		"GOCACHE=" + filepath.Join(root, ".gocache"),
		"GOMODCACHE=" + filepath.Join(root, ".gomodcache"),
		"GOPATH=" + filepath.Join(root, ".gopath"),
		"GOENV=off",
		"GOTELEMETRY=off",
		"GOTOOLCHAIN=local",
		"MPLCONFIGDIR=" + filepath.Join(root, ".mpl"),
		"PIP_CACHE_DIR=" + filepath.Join(root, ".pip-cache"),
		"DOTNET_CLI_HOME=" + filepath.Join(root, ".dotnet-home"),
		"NUGET_PACKAGES=" + filepath.Join(root, ".nuget"),
		"KONAN_USER_HOME=" + filepath.Join(root, ".konan-home"),
		"KONAN_DATA_DIR=" + filepath.Join(root, ".konan"),
		"MIX_HOME=" + filepath.Join(root, ".mix"),
		"HEX_HOME=" + filepath.Join(root, ".hex"),
		"JULIA_DEPOT_PATH=" + filepath.Join(root, ".julia"),
		"JULIA_PROBE_LIBSTDCXX=0",
		"R_HOME=/usr/lib/R",
		"R_SHARE_DIR=/usr/share/R/share",
		"R_INCLUDE_DIR=/usr/share/R/include",
		"R_DOC_DIR=/usr/share/R/doc",
		"R_DEFAULT_PACKAGES=NULL",
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

func TestOpenFileLimitForCommandRaisesKnownRuntimeNeeds(t *testing.T) {
	tests := []struct {
		command string
		want    int
	}{
		{"/opt/dotnet/dotnet", 512},
		{"/usr/bin/Rscript", 256},
		{"/usr/lib/R/bin/exec/R", 256},
		{"/usr/bin/python3", 64},
	}

	for _, tc := range tests {
		if got := OpenFileLimitForCommand(tc.command); got != tc.want {
			t.Fatalf("OpenFileLimitForCommand(%q) = %d, want %d", tc.command, got, tc.want)
		}
	}
}
