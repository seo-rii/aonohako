package execute

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestPipelineObservationBindsResolvedContestantAndLimits(t *testing.T) {
	req := &model.RunRequest{PythonLibraryMode: "stdlib", RuntimeProfile: "target",
		Pipeline: &model.PipelineV1{Programs: []model.RunProgram{
			{ID: "compute", Lang: "CPP20", Binaries: []model.Binary{{Name: "Main"}}},
			{ID: "python", Lang: "PYTHON3", Binaries: []model.Binary{{Name: "Main.py"}}},
		}},
	}
	step := model.PipelineStep{Executor: model.PipelineExecutor{Kind: "batch", ProgramID: "compute"},
		Limits: model.Limits{TimeMs: 10, MemoryMB: 16, WallTimeMs: 200, OutputBytes: 200},
		Outputs: []model.PipelineOutput{
			{Source: model.PipelineOutputSource{Kind: "participant_file", Path: "payload.bin"}, MaxBytes: 108},
			{Source: model.PipelineOutputSource{Kind: "participant_stdout"}, MaxBytes: 1024},
		},
	}
	plain := pipelineObservationRequest(req, step)
	if plain == nil || plain.Lang != "CPP20" || plain.Binaries[0].Name != "Main" ||
		plain.PythonLibraryMode != "" || plain.RuntimeProfile != "target" || plain.Limits.TimeMs != 10 ||
		plain.Limits.OutputBytes != 1024 || plain.FileOutputs[0].Path != "payload.bin" || step.Limits.OutputBytes != 200 {
		t.Fatalf("wrong batch binding: %+v", plain)
	}
	step.Executor.ProgramID = "python"
	if py := pipelineObservationRequest(req, step); py.Lang != "PYTHON3" || py.PythonLibraryMode != "stdlib" {
		t.Fatalf("Python step binding = %+v", py)
	}
	step.Executor.ProgramID = "missing"
	if pipelineObservationRequest(req, step) != nil {
		t.Fatal("unknown program resolved")
	}
	step.Executor = model.PipelineExecutor{Kind: "interactive", ParticipantProgramID: "compute", InteractorProgramID: "python"}
	if target := pipelineObservationRequest(req, step); target == nil || target.Lang != "CPP20" || target.Interactor != nil ||
		target.FileOutputs[0].Path != "payload.bin" {
		t.Fatal("interactive replay must resolve the original contestant alone")
	}
}

func TestPipelineReplayRequiresCompleteStdoutAndDeclaredFile(t *testing.T) {
	cfg := observerConfig()
	stdout := []byte{'A', 0, 'B'}
	file := []byte{0xff, 'x'}
	for _, tc := range []struct {
		name, change       string
		withFile, complete bool
	}{
		{"stdout", "", false, true},
		{"stdout and file", "", true, true},
		{"changed stdout", "stdout", false, false},
		{"changed stdout alongside matching file", "stdout", true, false},
		{"changed file alongside matching stdout", "file", true, false},
		{"truncated stdout alongside matching file", "truncated", true, false},
		{"exhausted", "exhausted", false, false},
		{"wrong profile", "profile", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := observerRequest()
			report := completeEnvelopeReport()
			report.CPUIDProfile = cfg.CPUIDProfile
			outputs := pipelineOutputDigests(stdout, nil)
			run := sandboxRunResult{response: model.RunResponse{WorkMeasurement: report,
				WorkMeasurementExecution: &model.WorkMeasurementExecution{State: "complete"}}, judgeOut: stdout}
			if tc.withFile {
				req.FileOutputs = []model.OutputFile{{Path: "payload"}}
				outputs = pipelineOutputDigests(stdout, file)
				run.stdoutOut, run.judgeOut = stdout, file
			}
			switch tc.change {
			case "stdout":
				if tc.withFile {
					run.stdoutOut = []byte("private altered stdout")
				} else {
					run.judgeOut = []byte("private altered stdout")
				}
			case "file":
				run.judgeOut = []byte("private altered payload")
			case "truncated":
				run.stdoutLimitExceeded = true
			case "exhausted":
				run.response.WorkMeasurementExecution.State = "resource_exhausted"
			case "profile":
				report.CPUIDProfile = ""
			}
			got, reason := batchReplayReport(req, outputs, run, cfg)
			if (got != nil) != tc.complete || (reason == "") != tc.complete || strings.Contains(reason, "private") {
				t.Fatalf("got report=%v, reason=%q", got != nil, reason)
			}
		})
	}
}

func TestPipelineObservationUsesOnlyStartedStepsAndPublicReasons(t *testing.T) {
	req := syntheticInteractiveBatchPipeline(100)
	// The second step has not started. The redacted result only identifies the first.
	first := req.Pipeline.Steps[0]
	started := []model.StepResult{{ID: first.ID, ProgramID: first.Executor.ParticipantProgramID, Status: model.RunStatusRE, Stderr: "private transcript"}}
	service := &Service{workMeasurement: observerConfig()}
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelled {
			cancel()
		}
		observation := service.observePipelineSteps(ctx, req, started, nil, nil, config.RuntimeTuningConfig{})
		cancel()
		if observation.Authority != durante.Authority || observation.Version != 2 || observation.State != "pipeline" ||
			len(observation.Steps) != 1 || observation.Steps[0].StepID != first.ID ||
			observation.Steps[0].ProgramID != first.Executor.ParticipantProgramID || observation.Steps[0].State != "unavailable" ||
			observation.Steps[0].Reason == "" || observation.Steps[0].Report != nil {
			t.Fatalf("invalid step coverage: %+v", observation)
		}
		wire, err := json.Marshal(observation)
		if err != nil || strings.Contains(string(wire), "private transcript") {
			t.Fatal("private native bytes escaped")
		}
	}
}

func TestPipelineReplayInputRebuildsBinaryNativeHandoffAfterUnlink(t *testing.T) {
	store, err := newPipelineArtifactStore()
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	fromNative := []byte{0xff, 0x00, 0xf3}
	if err := store.put("native", fromNative, 64); err != nil {
		t.Fatal(err)
	}
	inputs := executionPlanStep{limits: envelopeRequest().Limits, stdin: []executionPlanInput{
		{resource: []byte{0x01}}, {artifactID: "native"}, {resource: []byte{0xf0}},
	}}
	want := []byte{0x01, 0xff, 0x00, 0xf3, 0xf0}
	for _, pass := range []string{"native", "replay"} {
		f, _, err := preparePipelineStdin(inputs, store)
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil || !bytes.Equal(contents, want) {
			t.Fatalf("%s stdin = %x, error %v", pass, contents, err)
		}
		if err := os.Remove(f.Name()); err != nil {
			t.Fatal(err)
		}
		if pass == "native" {
			// Later native stages may publish additional outputs before replay.
			if err := store.put("final", []byte("different"), 64); err != nil {
				t.Fatal(err)
			}
		}
	}
}
