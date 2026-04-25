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
