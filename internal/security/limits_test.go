package security

import "testing"

func TestFileSizeLimitForCommandDoesNotOverrideDotnet(t *testing.T) {
	if got := FileSizeLimitForCommand("/opt/dotnet/dotnet", 0); got != 0 {
		t.Fatalf("dotnet file size override = %d, want 0", got)
	}
	if got := FileSizeLimitForCommand("/usr/bin/python3", 128<<20); got != 0 {
		t.Fatalf("python file size override = %d, want 0", got)
	}
}

func TestStackLimitForCommandKeepsDotnetCompatible(t *testing.T) {
	if got := StackLimitForCommand("/opt/dotnet/dotnet"); got != 64*1024*1024 {
		t.Fatalf("dotnet stack limit = %d, want 64MiB", got)
	}
	if got := StackLimitForCommand("/usr/bin/python3"); got != 8*1024*1024 {
		t.Fatalf("python stack limit = %d, want 8MiB", got)
	}
}
