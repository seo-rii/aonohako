package docscontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPayloadDocMatchesRuntimeLimitsAndModes(t *testing.T) {
	body := mustRead(t, filepath.Join("..", "..", "docs", "payload.md"))

	wants := []string{
		`"exec" → chmod 0555; otherwise chmod 0444`,
		`Accepted|Wrong Answer|Time Limit Exceeded|Memory Limit Exceeded|Workspace Limit Exceeded|Runtime Error|Container Initialization Failed`,
		"`prlimit --as` | Virtual address space (memory_mb + 64 MB, min 512 MB)",
		"`prlimit --fsize` | Max file size (workspace_bytes when set, otherwise 128 MB)",
		"at most one path is supported",
		"capture failure is reported as `Runtime Error`",
	}

	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Fatalf("payload.md missing %q", want)
		}
	}
}

func TestProtocolAndArchitectureDocsMatchQueueLoggingAndFDSemantics(t *testing.T) {
	protocol := mustRead(t, filepath.Join("..", "..", "docs", "protocol.md"))
	architecture := mustRead(t, filepath.Join("..", "..", "docs", "architecture.md"))

	protocolWants := []string{
		"Both `/compile` and `/execute` share the same bounded queue",
		"buffered stdout / stderr payloads emitted before `result`",
		"Workspace Limit Exceeded",
		"truncated stdout (up to `limits.output_bytes`; default `64 KiB`, hard cap `8 MiB`)",
		"`AONOHAKO_EXECUTION_MODE=cloudrun`",
	}
	for _, want := range protocolWants {
		if !strings.Contains(protocol, want) {
			t.Fatalf("protocol.md missing %q", want)
		}
	}

	if !strings.Contains(architecture, "`CloseRange(3, ..., CLOSE_RANGE_CLOEXEC)` fallback `CloseOnExec` loop") {
		t.Fatalf("architecture.md must describe CLOEXEC fd inheritance behavior")
	}
	if !strings.Contains(architecture, "hardens shared scratch directories") {
		t.Fatalf("architecture.md must describe startup scratch hardening")
	}
	if !strings.Contains(architecture, "fail closed unless all of the following are") || !strings.Contains(architecture, "true before the HTTP server starts") {
		t.Fatalf("architecture.md must describe startup deployment contract validation")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
