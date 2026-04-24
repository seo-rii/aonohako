package security

import "testing"

func TestFileSizeLimitForCommandKeepsDotnetFinite(t *testing.T) {
	if got := FileSizeLimitForCommand("/opt/dotnet/dotnet", 0); got != dotnetFileSizeLimitBytes {
		t.Fatalf("dotnet file size limit = %d, want %d", got, dotnetFileSizeLimitBytes)
	}
	if got := FileSizeLimitForCommand("/usr/bin/python3", 128<<20); got != 0 {
		t.Fatalf("python file size override = %d, want 0", got)
	}
}

func TestFileSizeLimitForCommandAllowsLargerWorkspace(t *testing.T) {
	workspaceLimit := int64(768 << 20)
	if got := FileSizeLimitForCommand("dotnet", workspaceLimit); got != uint64(workspaceLimit) {
		t.Fatalf("dotnet file size limit = %d, want %d", got, workspaceLimit)
	}
}
