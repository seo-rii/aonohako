package security

import "testing"

func TestOpenFileLimitForCommandKeepsDotnetCompatible(t *testing.T) {
	if got := OpenFileLimitForCommand("/opt/dotnet/dotnet"); got != 512 {
		t.Fatalf("dotnet open file limit = %d, want 512", got)
	}
	if got := OpenFileLimitForCommand("/usr/local/bin/dafny"); got != 512 {
		t.Fatalf("dafny open file limit = %d, want 512", got)
	}
	if got := OpenFileLimitForCommand("/usr/local/bin/aonohako-tla-run"); got != 512 {
		t.Fatalf("tla open file limit = %d, want 512", got)
	}
	if got := OpenFileLimitForCommand("/usr/local/bin/isabelle"); got != 512 {
		t.Fatalf("isabelle open file limit = %d, want 512", got)
	}
	for _, command := range []string{
		"/usr/local/bin/fstar.exe",
		"/usr/local/bin/aonohako-alloy-check",
		"/usr/local/bin/aonohako-acl2-check",
		"/usr/local/bin/aonohako-kframework-check",
	} {
		if got := OpenFileLimitForCommand(command); got != 512 {
			t.Fatalf("%s open file limit = %d, want 512", command, got)
		}
	}
	if got := OpenFileLimitForCommand("/usr/bin/python3"); got != 64 {
		t.Fatalf("python open file limit = %d, want 64", got)
	}
}

func TestFileSizeLimitForCommandKeepsDotnetFinite(t *testing.T) {
	if got := FileSizeLimitForCommand("/opt/dotnet/dotnet", 0); got != DotnetFileSizeLimitBytes {
		t.Fatalf("dotnet file size override = %d, want %d", got, DotnetFileSizeLimitBytes)
	}
	if got := FileSizeLimitForCommand("dafny", 0); got != DotnetFileSizeLimitBytes {
		t.Fatalf("dafny file size override = %d, want %d", got, DotnetFileSizeLimitBytes)
	}
	workspaceLimit := int64(DotnetFileSizeLimitBytes + (512 << 20))
	if got := FileSizeLimitForCommand("dotnet", workspaceLimit); got != uint64(workspaceLimit) {
		t.Fatalf("dotnet file size override = %d, want %d", got, workspaceLimit)
	}
	if got := FileSizeLimitForCommand("/usr/bin/python3", 128<<20); got != 0 {
		t.Fatalf("python file size override = %d, want 0", got)
	}
}

func TestDotnetFileSizeLimitIsCompatibilityCapNotWorkspaceQuota(t *testing.T) {
	workspaceLimit := int64(512 << 20)
	for _, command := range []string{"dotnet", "dafny"} {
		got := FileSizeLimitForCommand(command, workspaceLimit)
		if got != DotnetFileSizeLimitBytes {
			t.Fatalf("%s file size limit = %d, want compatibility cap %d", command, got, DotnetFileSizeLimitBytes)
		}
		if got <= uint64(workspaceLimit) {
			t.Fatalf("%s file size limit should remain above workspace quota so CoreCLR startup is not bounded by workspace bytes", command)
		}
	}
}

func TestDotnetFileSizeCompatibilityCapExceedsDefaultWorkspaceQuota(t *testing.T) {
	defaultWorkspaceLimit := int64(128 << 20)
	if DotnetFileSizeLimitBytes <= uint64(defaultWorkspaceLimit) {
		t.Fatalf("dotnet compatibility cap should stay above default workspace quota")
	}
	for _, command := range []string{"dotnet", "dafny"} {
		if got := FileSizeLimitForCommand(command, defaultWorkspaceLimit); got != DotnetFileSizeLimitBytes {
			t.Fatalf("%s file size limit = %d, want compatibility cap %d", command, got, DotnetFileSizeLimitBytes)
		}
	}
}

func TestStackLimitForCommandUsesInheritedHardLimit(t *testing.T) {
	for _, command := range []string{
		"/opt/dotnet/dotnet",
		"/usr/local/bin/dafny",
		"/usr/local/bin/aonohako-tla-run",
		"/usr/local/bin/isabelle",
		"/usr/local/bin/fstar.exe",
		"/usr/local/bin/aonohako-alloy-check",
		"/usr/local/bin/aonohako-acl2-check",
		"/usr/local/bin/aonohako-kframework-check",
		"/usr/bin/python3",
		"/tmp/Main",
	} {
		if got := StackLimitForCommand(command); got != 0 {
			t.Fatalf("%s stack limit = %d, want inherited hard limit", command, got)
		}
	}
}
