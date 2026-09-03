package execute

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

func TestRedactPipelineResponseRemovesAllPrivateDiagnostics(t *testing.T) {
	const secret = "PRIVATE-PIPELINE-DIAGNOSTIC"
	resp := RedactPipelineResponse(model.RunResponse{
		Status:          model.RunStatusRE,
		Stdout:          secret,
		Stderr:          secret,
		Reason:          secret,
		VerdictSource:   "step:phase2:exit_code",
		StdoutTruncated: true,
		Steps: []model.StepResult{{
			ID:              "phase2",
			Status:          model.RunStatusRE,
			Stdout:          secret,
			Stderr:          secret,
			Reason:          secret,
			VerdictSource:   "exit_code",
			HandoffBytes:    17,
			StderrTruncated: true,
		}},
	})

	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("private diagnostic survived pipeline redaction: %s", encoded)
	}
	if resp.Stdout != "" || resp.Stderr != "" || resp.Steps[0].Stdout != "" || resp.Steps[0].Stderr != "" {
		t.Fatalf("pipeline output survived redaction: %+v", resp)
	}
	if resp.Reason != "runtime error" || resp.Steps[0].Reason != "runtime error" {
		t.Fatalf("pipeline reasons are not safe generic diagnostics: %+v", resp)
	}
	if !resp.StdoutTruncated || !resp.Steps[0].StderrTruncated || resp.Steps[0].HandoffBytes != 17 {
		t.Fatalf("non-secret diagnostic metadata was lost: %+v", resp)
	}
}

func TestPipelineArtifactStoreKeepsBinaryDataPrivateAndBounded(t *testing.T) {
	store, err := newPipelineArtifactStore()
	if err != nil {
		t.Fatal(err)
	}
	dir := store.dir
	defer store.close()

	want := []byte{0, 1, 2, 0xff, '\n'}
	if err := store.put("binary", want, int64(len(want))); err != nil {
		t.Fatalf("put binary artifact: %v", err)
	}
	artifact, err := store.open("binary")
	if err != nil {
		t.Fatalf("open binary artifact: %v", err)
	}
	got, err := io.ReadAll(artifact)
	_ = artifact.Close()
	if err != nil {
		t.Fatalf("read binary artifact: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("artifact = %v, want %v", got, want)
	}
	if err := store.put("oversized", want, int64(len(want)-1)); err == nil || !strings.Contains(err.Error(), "max_bytes") {
		t.Fatalf("oversized artifact error = %v", err)
	}
	store.storedBytes = runvalidation.MaxPipelineArtifactTotalBytes
	if err := store.put("aggregate-overflow", []byte{1}, 1); err == nil || !strings.Contains(err.Error(), "total byte limit") {
		t.Fatalf("aggregate artifact error = %v", err)
	}

	store.close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("artifact store survived cleanup: %v", err)
	}
}

func TestCaptureInteractorOutputRejectsUntrustedFileKindsAndSize(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "output")
	if err := os.WriteFile(regular, []byte("private\x00artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	var copied bytes.Buffer
	if _, err := captureInteractorOutput(regular, &copied, 64); err != nil {
		t.Fatalf("capture regular interactor output: %v", err)
	}
	if got := copied.Bytes(); !bytes.Equal(got, []byte("private\x00artifact")) {
		t.Fatalf("captured output = %q", got)
	}
	if _, err := captureInteractorOutput(regular, io.Discard, 1); err == nil || !strings.Contains(err.Error(), "max_bytes") {
		t.Fatalf("oversized interactor output error = %v", err)
	}

	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := captureInteractorOutput(symlink, io.Discard, 64); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink interactor output error = %v", err)
	}

	directory := filepath.Join(dir, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := captureInteractorOutput(directory, io.Discard, 64); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory interactor output error = %v", err)
	}

	hardlink := filepath.Join(dir, "hardlink")
	if err := os.Link(regular, hardlink); err != nil {
		t.Skipf("hard links unsupported on test filesystem: %v", err)
	}
	if _, err := captureInteractorOutput(regular, io.Discard, 64); err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Fatalf("hard-linked interactor output error = %v", err)
	}
}

func syntheticInteractiveBatchPipeline(maxBytes int64) *model.RunRequest {
	participant := `import os
import sys

parts = sys.stdin.readline().strip().split()
if parts[0] == "phase1":
    with open("phase-marker", "w", encoding="utf-8") as marker:
        marker.write("private workspace state")
    print(int(parts[1]) + 1, os.getpid(), flush=True)
else:
    first_pid = int(parts[1])
    fresh = first_pid != os.getpid() and not os.path.exists("phase-marker")
    print(int(parts[0]) * 2, "fresh" if fresh else "reused")
`
	interactor := `import sys

input_path, output_path, answer_path = sys.argv[1:4]
with open(input_path, "r", encoding="utf-8") as source:
    original = int(source.read().strip())
with open(answer_path, "r", encoding="utf-8") as source:
    if source.read() != "sentinel\n":
        raise SystemExit(3)
print("phase1", original, flush=True)
answer, first_pid = sys.stdin.readline().strip().split()
if int(answer) != original + 1:
    raise SystemExit(1)
with open(output_path, "w", encoding="utf-8") as output:
    output.write("7 " + first_pid + "\n")
`
	spj := `#!/usr/bin/env python3
import sys

input_path, output_path, expected_path = sys.argv[1:4]
with open(input_path, "r", encoding="utf-8") as source:
    original = source.read()
with open(output_path, "r", encoding="utf-8") as source:
    actual = source.read()
with open(expected_path, "r", encoding="utf-8") as source:
    expected = source.read()
raise SystemExit(0 if original == "41\n" and actual == "14 fresh\n" and expected == "sentinel\n" else 1)
`
	limits := model.Limits{TimeMs: 3000, MemoryMB: 128, OutputBytes: 4096}
	return &model.RunRequest{
		Pipeline: &model.PipelineV1{
			Version: 1,
			Resources: map[string]model.PipelineResource{
				"testcase": {DataB64: b64("41\n")},
				"answer":   {DataB64: b64("sentinel\n")},
			},
			Programs: []model.RunProgram{
				{ID: "participant", Lang: "python", Binaries: []model.Binary{{Name: "main.py", DataB64: b64(participant)}}},
				{ID: "interactor", Lang: "python", Binaries: []model.Binary{{Name: "interactor.py", DataB64: b64(interactor)}}},
			},
			Steps: []model.PipelineStep{
				{
					ID: "phase1",
					Executor: model.PipelineExecutor{
						Kind:                 "interactive",
						ParticipantProgramID: "participant",
						InteractorProgramID:  "interactor",
						InteractorLimits:     &limits,
						InteractorAnswer:     &model.PipelineRef{Type: "resource", ID: "answer"},
					},
					Stdin:   []model.PipelineRef{{Type: "resource", ID: "testcase"}},
					Outputs: []model.PipelineOutput{{ID: "phase2-input", Source: model.PipelineOutputSource{Kind: "interactor_output"}, MaxBytes: maxBytes}},
					Limits:  limits,
				},
				{
					ID:       "phase2",
					Executor: model.PipelineExecutor{Kind: "batch", ProgramID: "participant"},
					Stdin:    []model.PipelineRef{{Type: "artifact", ID: "phase2-input"}},
					Limits:   limits,
				},
			},
			FinalJudge: model.PipelineFinalJudge{
				Kind:     "spj",
				Input:    model.PipelineRef{Type: "resource", ID: "testcase"},
				Expected: model.PipelineRef{Type: "resource", ID: "answer"},
				Actual:   model.PipelineRef{Type: "step_stdout", StepID: "phase2"},
				SPJ:      &model.SPJSpec{Lang: "python", Binary: &model.Binary{Name: "spj.py", DataB64: b64(spj)}, Limits: &limits},
			},
		},
	}
}

func TestRunPipelineInteractiveArtifactBatchAndExplicitOriginalInputSPJ(t *testing.T) {
	requireSandboxSupport(t)

	resp := New().Run(context.Background(), syntheticInteractiveBatchPipeline(4096), Hooks{})
	if resp.Status != model.RunStatusAccepted || resp.VerdictSource != "final:spj" {
		t.Fatalf("pipeline response = %+v, want accepted final SPJ verdict", resp)
	}
	if len(resp.Steps) != 2 || resp.Steps[0].ID != "phase1" || resp.Steps[1].ID != "phase2" {
		t.Fatalf("pipeline steps = %+v", resp.Steps)
	}
	if resp.Steps[0].HandoffBytes == 0 {
		t.Fatalf("interactor artifact metadata missing: %+v", resp.Steps[0])
	}
	if resp.Stdout != "" || resp.Steps[0].Stdout != "" || resp.Steps[1].Stdout != "" {
		t.Fatalf("private pipeline output leaked in response: %+v", resp)
	}
}

func TestRunPipelineRejectsOversizedInteractorArtifactBeforeSecondStep(t *testing.T) {
	requireSandboxSupport(t)

	resp := New().Run(context.Background(), syntheticInteractiveBatchPipeline(1), Hooks{})
	if resp.Status != model.RunStatusRE || resp.VerdictSource != "step:phase1:artifact" {
		t.Fatalf("pipeline response = %+v, want phase1 artifact failure", resp)
	}
	if len(resp.Steps) != 1 {
		t.Fatalf("later pipeline step ran after artifact failure: %+v", resp.Steps)
	}
	if resp.Reason != "pipeline artifact processing failed" {
		t.Fatalf("artifact failure reason = %q, want generic public diagnostic", resp.Reason)
	}
}

func TestRunPipelineDoesNotExposeFailedStepStderrOrHooks(t *testing.T) {
	requireSandboxSupport(t)

	req := syntheticInteractiveBatchPipeline(4096)
	participant := `import os
import sys

parts = sys.stdin.readline().strip().split()
if parts[0] == "phase1":
    print(int(parts[1]) + 1, os.getpid(), flush=True)
else:
    sys.stderr.write("PRIVATE-PIPELINE-DIAGNOSTIC:" + " ".join(parts))
    raise SystemExit(1)
`
	req.Pipeline.Programs[0].Binaries[0].DataB64 = b64(participant)
	var logCalled atomic.Bool
	var imageCalled atomic.Bool
	resp := New().Run(context.Background(), req, Hooks{
		OnLog:   func(string, string) { logCalled.Store(true) },
		OnImage: func(string, string, int64) { imageCalled.Store(true) },
	})
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PRIVATE-PIPELINE-DIAGNOSTIC") {
		t.Fatalf("private failed-step diagnostic leaked: %s", encoded)
	}
	if logCalled.Load() || imageCalled.Load() {
		t.Fatalf("pipeline emitted private hook data: log=%v image=%v", logCalled.Load(), imageCalled.Load())
	}
	if resp.Status != model.RunStatusRE || resp.Reason != "runtime error" || resp.VerdictSource != "step:phase2:exit_code" {
		t.Fatalf("unexpected redacted pipeline failure: %+v", resp)
	}
}

func TestRunPipelineRejectsTruncatedInteractiveParticipantStdoutBeforeArtifacts(t *testing.T) {
	requireSandboxSupport(t)

	req := syntheticInteractiveBatchPipeline(4096)
	req.Pipeline.Steps[0].Limits.OutputBytes = 1
	req.Pipeline.Steps[0].Outputs = append(req.Pipeline.Steps[0].Outputs, model.PipelineOutput{
		ID:       "participant-transcript",
		Source:   model.PipelineOutputSource{Kind: "participant_stdout"},
		MaxBytes: 1,
	})
	resp := New().Run(context.Background(), req, Hooks{})
	if resp.Status != model.RunStatusRE || resp.VerdictSource != "step:phase1:participant:stdout_limit" {
		t.Fatalf("pipeline response = %+v, want fail-closed interactive stdout limit", resp)
	}
	if len(resp.Steps) != 1 || resp.Steps[0].HandoffBytes != 0 {
		t.Fatalf("artifact or later step survived interactive stdout truncation: %+v", resp.Steps)
	}
	if resp.Stdout != "" || resp.Stderr != "" || resp.Reason != "runtime error" {
		t.Fatalf("interactive stdout-limit diagnostics were not redacted: %+v", resp)
	}
}

func TestRunPipelineCancellationStopsInteractivePeersAndKeepsDiagnosticsPrivate(t *testing.T) {
	requireSandboxSupport(t)

	req := syntheticInteractiveBatchPipeline(4096)
	participant := `import sys
import time

sys.stderr.write("PRIVATE-CANCELED-DIAGNOSTIC")
sys.stderr.flush()
time.sleep(30)
`
	req.Pipeline.Programs[0].Binaries[0].DataB64 = b64(participant)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(250*time.Millisecond, cancel)
	started := time.Now()
	resp := New().Run(ctx, req, Hooks{})
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("pipeline cancellation took %s", elapsed)
	}
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PRIVATE-CANCELED-DIAGNOSTIC") {
		t.Fatalf("canceled pipeline leaked diagnostics: %s", encoded)
	}
	if resp.Status == model.RunStatusAccepted || len(resp.Steps) > 1 {
		t.Fatalf("pipeline continued after cancellation: %+v", resp)
	}
}
