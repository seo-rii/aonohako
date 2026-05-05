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

func TestStackLimitForCommandKeepsDotnetCompatible(t *testing.T) {
	if got := StackLimitForCommand("/opt/dotnet/dotnet"); got != 64*1024*1024 {
		t.Fatalf("dotnet stack limit = %d, want 64MiB", got)
	}
	if got := StackLimitForCommand("/usr/local/bin/dafny"); got != 64*1024*1024 {
		t.Fatalf("dafny stack limit = %d, want 64MiB", got)
	}
	if got := StackLimitForCommand("/usr/local/bin/aonohako-tla-run"); got != 64*1024*1024 {
		t.Fatalf("tla stack limit = %d, want 64MiB", got)
	}
	if got := StackLimitForCommand("/usr/local/bin/isabelle"); got != 64*1024*1024 {
		t.Fatalf("isabelle stack limit = %d, want 64MiB", got)
	}
	if got := StackLimitForCommand("/usr/bin/python3"); got != 8*1024*1024 {
		t.Fatalf("python stack limit = %d, want 8MiB", got)
	}
}
