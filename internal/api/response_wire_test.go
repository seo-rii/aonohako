package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

func TestExecuteRemoteResultPreservesVerdictAndArtifactsAtWireLimit(t *testing.T) {
	const captureBytes = 6 << 20
	output := strings.Repeat("\x00", captureBytes)
	// Each file satisfies the 8 MiB per-file limit and together they fill the
	// 16 MiB sidecar budget. Keep artifacts intact when display text is clipped.
	artifact := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, 8<<20))
	for _, tc := range []struct {
		name     string
		legacy   bool
		emitLogs bool
	}{
		{name: "single_without_logs"},
		{name: "legacy_steps_with_logs", legacy: true, emitLogs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exitCode, score := 7, 0.375
			original := model.RunResponse{
				Status: model.RunStatusRE, Reason: "process exited with code 7", VerdictSource: "exit_code",
				ExitCode: &exitCode, Score: &score, TimeMs: 5, WallTimeMs: 7, CPUTimeMs: 3, MemoryKB: 128,
				Stdout: output, Stderr: output,
				SidecarOutputs: []model.SidecarOutput{
					{Path: "first.bin", DataB64: artifact},
					{Path: "second.bin", DataB64: artifact},
				},
			}
			if tc.legacy {
				original.Steps = []model.StepResult{
					{ID: "first", ProgramID: "first", Status: model.RunStatusAccepted, Stdout: output, Stderr: "short diagnostic\n", StderrTruncated: true, HandoffBytes: captureBytes},
					{ID: "second", ProgramID: "second", Status: model.RunStatusRE, Stdout: output, Stderr: output, ExitCode: &exitCode, Reason: original.Reason, VerdictSource: original.VerdictSource},
				}
			}
			var calls atomic.Int64
			server := NewWithServices(configForTest(t), compileRunnerStub{}, executeRunnerStub{run: func(_ context.Context, forwarded *model.RunRequest, hooks execute.Hooks) model.RunResponse {
				calls.Add(1)
				if forwarded.EmitLogs == nil || *forwarded.EmitLogs != tc.emitLogs ||
					forwarded.CaptureLimits == nil || forwarded.CaptureLimits.StdoutBytes == nil || forwarded.CaptureLimits.StderrBytes == nil ||
					*forwarded.CaptureLimits.StdoutBytes != captureBytes || *forwarded.CaptureLimits.StderrBytes != captureBytes {
					t.Error("remote request lost emit_logs or per-stream capture limits")
				}
				if (hooks.OnLog != nil) != tc.emitLogs {
					t.Errorf("OnLog present = %v, emit_logs = %v", hooks.OnLog != nil, tc.emitLogs)
				}
				if hooks.OnLog != nil {
					hooks.OnLog("stdout", "\x00\"\\\n")
					hooks.OnLog("stderr", "diagnostic\n")
				}
				return original
			}})
			remote := httptest.NewServer(server.Handler())
			defer remote.Close()
			cfg := configForTest(t)
			cfg.Execution.Platform.ExecutionTransport = platform.ExecutionTransportRemote
			cfg.Execution.Platform.SandboxBackend = platform.SandboxBackendNone
			cfg.Execution.Remote.URL = remote.URL
			cfg.Execution.Remote.StrictProtocol = true
			runner, err := execute.Build(cfg)
			if err != nil {
				t.Fatal(err)
			}
			capture := captureBytes
			limits := model.Limits{TimeMs: 1000, MemoryMB: 64, OutputBytes: 8 << 20}
			req := &model.RunRequest{
				Lang: "binary", Binaries: []model.Binary{{Name: "program", DataB64: "eA==", Mode: "exec"}},
				Limits: limits, CaptureLimits: &model.CaptureLimits{StdoutBytes: &capture, StderrBytes: &capture},
				SidecarOutputs: []model.OutputFile{{Path: "first.bin"}, {Path: "second.bin"}}, EmitLogs: &tc.emitLogs,
			}
			if tc.legacy {
				req.Lang, req.Binaries, req.Limits = "", nil, model.Limits{}
				req.Programs = []model.RunProgram{
					{ID: "first", Lang: "binary", Binaries: []model.Binary{{Name: "first", DataB64: "eA==", Mode: "exec"}}},
					{ID: "second", Lang: "binary", Binaries: []model.Binary{{Name: "second", DataB64: "eA==", Mode: "exec"}}},
				}
				req.Steps = []model.RunStep{
					{ID: "first", ProgramID: "first", Limits: limits, Handoff: &model.StepHandoff{ID: "handoff", From: "stdout", MaxBytes: captureBytes}},
					{ID: "second", ProgramID: "second", StdinFrom: "handoff", Limits: limits},
				}
			}
			logs := make(map[string]string)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			got := runner.Run(ctx, req, execute.Hooks{OnLog: func(stream, msg string) { logs[stream] += msg }})
			if got.Status != original.Status || got.Reason != original.Reason || got.VerdictSource != original.VerdictSource ||
				got.ExitCode == nil || *got.ExitCode != exitCode || got.Score == nil || *got.Score != score ||
				got.TimeMs != original.TimeMs || got.WallTimeMs != original.WallTimeMs || got.CPUTimeMs != original.CPUTimeMs || got.MemoryKB != original.MemoryKB {
				t.Fatalf("wire transport changed verdict or measurements: status=%q reason=%q source=%q exit=%v score=%v", got.Status, got.Reason, got.VerdictSource, got.ExitCode, got.Score)
			}
			if calls.Load() != 1 {
				t.Fatalf("remote execution calls = %d, want one", calls.Load())
			}
			if len(got.SidecarOutputs) != len(original.SidecarOutputs) {
				t.Fatalf("wire transport returned %d sidecars, want %d", len(got.SidecarOutputs), len(original.SidecarOutputs))
			}
			for i, sidecar := range got.SidecarOutputs {
				if sidecar != original.SidecarOutputs[i] {
					t.Fatalf("wire clipping changed sidecar %d", i)
				}
			}
			if len(got.Steps) != len(original.Steps) {
				t.Fatalf("wire transport returned %d steps, want %d", len(got.Steps), len(original.Steps))
			}
			type displayCapture struct {
				name, original, got string
				already, truncated  bool
			}
			fields := []displayCapture{
				{name: "stdout", original: output, got: got.Stdout, truncated: got.StdoutTruncated},
				{name: "stderr", original: output, got: got.Stderr, truncated: got.StderrTruncated},
			}
			for i, step := range got.Steps {
				before := original.Steps[i]
				if step.ID != before.ID || step.ProgramID != before.ProgramID || step.Status != before.Status ||
					step.Reason != before.Reason || step.VerdictSource != before.VerdictSource || step.HandoffBytes != before.HandoffBytes {
					t.Fatalf("wire clipping changed step %d metadata", i)
				}
				fields = append(fields,
					displayCapture{name: step.ID + "/stdout", original: before.Stdout, got: step.Stdout, already: before.StdoutTruncated, truncated: step.StdoutTruncated},
					displayCapture{name: step.ID + "/stderr", original: before.Stderr, got: step.Stderr, already: before.StderrTruncated, truncated: step.StderrTruncated})
			}
			for _, field := range fields {
				if !strings.HasPrefix(field.original, field.got) || field.truncated != (field.already || len(field.got) < len(field.original)) {
					t.Fatalf("%s prefix/flag changed: bytes=%d/%d flag=%v", field.name, len(field.got), len(field.original), field.truncated)
				}
				if len(field.original) == captureBytes && (len(field.got) == 0 || len(field.got) == captureBytes) {
					t.Fatalf("%s should retain a non-empty clipped prefix, got %d bytes", field.name, len(field.got))
				}
			}
			if tc.legacy && got.Steps[0].Stderr != original.Steps[0].Stderr {
				t.Fatal("wire clipping discarded a small diagnostic that fits")
			}
			if tc.emitLogs {
				if logs["stdout"] != "\x00\"\\\n" || logs["stderr"] != "diagnostic\n" {
					t.Fatal("enabled logs changed during remote transfer")
				}
			} else if len(logs) != 0 {
				t.Fatal("emit_logs=false emitted logs")
			}
			if original.Stdout != output || original.Stderr != output || original.StdoutTruncated || original.StderrTruncated ||
				(tc.legacy && (original.Steps[0].Stdout != output || original.Steps[0].StdoutTruncated)) {
				t.Fatal("wire clipping mutated the runner's response")
			}
		})
	}
}
