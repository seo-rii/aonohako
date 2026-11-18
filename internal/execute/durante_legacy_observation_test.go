package execute

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"aonohako/internal/config"
	"aonohako/internal/model"
)

func TestLegacyObservationBindsActualProgramAndHandoff(t *testing.T) {
	req := &model.RunRequest{
		Programs: []model.RunProgram{
			{ID: " a ", Lang: "CPP20", Binaries: []model.Binary{{Name: "first"}}},
			{ID: "b", Lang: "PYTHON3", Binaries: []model.Binary{{Name: "second"}}},
		},
		Steps: []model.RunStep{
			{ID: "1", ProgramID: "a", Stdin: "01\x00\n", Handoff: &model.StepHandoff{ID: "h", From: " FILE_OUTPUT ", Path: "handoff.bin", MaxBytes: 108}},
			{ID: "2", ProgramID: "b", StdinFrom: "h"},
		},
		ExpectedStdout: "output", SPJ: &model.SPJSpec{Lang: "PYTHON3"},
		FileOutputs: []model.OutputFile{{Path: "final.bin"}}, SidecarOutputs: []model.OutputFile{{Path: "sidecar"}},
		IgnoreTLE: true,
	}
	first, last := legacyStepRequest(req, 0), legacyStepRequest(req, 1)
	if first.Lang != "CPP20" || first.Binaries[0].Name != "first" || first.Stdin != req.Steps[0].Stdin ||
		first.Limits != effectiveStepLimits(req.Steps[0]) || first.FileOutputs[0].Path != "handoff.bin" ||
		first.ExpectedStdout != "" || first.SPJ != nil || first.IgnoreTLE || len(first.SidecarOutputs) != 0 {
		t.Fatalf("wrong first stage: %+v", first)
	}
	if last.Lang != "PYTHON3" || last.ExpectedStdout != "output" || last.SPJ != req.SPJ ||
		last.FileOutputs[0].Path != "final.bin" || !last.IgnoreTLE || len(last.SidecarOutputs) != 1 {
		t.Fatalf("wrong final stage: %+v", last)
	}
	if legacyStepRequest(req, -1) != nil || legacyStepRequest(req, 2) != nil {
		t.Fatal("resolved stage outside the pipeline")
	}
	req.Steps[0].ProgramID = "missing"
	if legacyStepRequest(req, 0) != nil {
		t.Fatal("resolved an unknown program")
	}
}

func TestLegacyURLSnapshotPreservesNativeReadsAndErrors(t *testing.T) {
	failure := errors.New("native reader failed")
	for _, tc := range []struct {
		name string
		data []byte
		fail bool
		over bool
	}{
		{"empty", nil, false, false},
		{"binary", []byte{'a', 0, 0xff, 'b'}, false, false},
		{"exact cap", bytes.Repeat([]byte{0xa5}, legacyURLSnapshotBytes), false, false},
		{"over cap", bytes.Repeat([]byte{0x5a}, legacyURLSnapshotBytes+1), false, true},
		{"partial transfer", []byte("partial"), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var source io.Reader = bytes.NewReader(tc.data)
			if tc.fail {
				source = io.MultiReader(source, iotest.ErrReader(failure))
			}
			replay, reader := prepareLegacyReplay(&model.RunRequest{}, source, legacyURLSnapshotBytes+1024)
			got, err := io.ReadAll(reader)
			if !bytes.Equal(got, tc.data) || (tc.fail && !errors.Is(err, failure)) || (!tc.fail && err != nil) {
				t.Fatal("observation changed native stream")
			}
			if replay.url == nil || replay.url.over != tc.over || replay.url.eof == tc.fail {
				t.Fatal("incorrect snapshot completion")
			}
			if tc.over && replay.url.bytes != nil || !tc.over && !bytes.Equal(replay.url.bytes, tc.data) {
				t.Fatal("incorrect snapshot bytes")
			}
		})
	}
}

func TestLegacyObservationUsesNativeMaterializedInput(t *testing.T) {
	dir := t.TempDir()
	handoff := filepath.Join(dir, "h")
	if err := os.WriteFile(handoff, []byte{0x00, 0xff, 'z'}, 0o600); err != nil {
		t.Fatal(err)
	}
	step := model.RunStep{StdinParts: []model.StdinPart{{Type: "text", Data: "A"}, {Type: "handoff", ID: "h"}, {Type: "text", Data: "\n"}}}
	_, input, closeInput, _, maxBytes, err := prepareStepStdin(context.Background(), step, map[string]string{"h": handoff}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay, native := prepareLegacyReplay(&model.RunRequest{}, input, maxBytes)
	nativeData, err := io.ReadAll(native)
	closeErr := closeInput()
	if err != nil || closeErr != nil || replay.url != nil {
		t.Fatalf("unable to use native assembled file: read=%v close=%v", err, closeErr)
	}
	if err := os.Remove(handoff); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(replay.path)
	if err != nil || !bytes.Equal(data, nativeData) || !bytes.Equal(data, []byte{'A', 0x00, 0xff, 'z', '\n'}) {
		t.Fatalf("replay did not retain exact assembled file: %x, %v", data, err)
	}
}

func TestLegacyObservationRequiresFinishedStepAndPrivateSource(t *testing.T) {
	zero := 0
	single := &model.RunRequest{}
	row := model.StepResult{ID: "first", ProgramID: "a", Status: model.RunStatusAccepted, ExitCode: &zero}
	if !legacyNativeStepOutput(single, row, false) || legacyNativeStepOutput(single, row, true) {
		t.Fatal("confused handoff and final judging")
	}
	row.Status, row.VerdictSource = model.RunStatusWA, "stdout"
	if !legacyNativeStepOutput(single, row, true) {
		t.Fatal("completed WA lost its native output comparison")
	}
	row.StdoutTruncated = true
	if !legacyNativeStepOutput(single, row, true) {
		t.Fatal("rejected display-only stdout truncation on WA")
	}
	row.VerdictSource = "spj"
	single.SPJ = &model.SPJSpec{}
	if !legacyNativeStepOutput(single, row, true) {
		t.Fatal("rejected display-only truncation after a stdout SPJ completed")
	}
	single.FileOutputs = []model.OutputFile{{Path: "out"}}
	if legacyNativeStepOutput(single, row, true) {
		t.Fatal("qualified a file verdict with ambiguous execution truncation")
	}
	row.StdoutTruncated = false
	row.Status = model.RunStatusTLE
	if legacyNativeStepOutput(single, row, true) {
		t.Fatal("qualified interrupted output")
	}
	row.Status, row.VerdictSource = model.RunStatusAccepted, ""
	s := &Service{}
	primary := model.RunResponse{Status: model.RunStatusRE, VerdictSource: "step:first:handoff", Steps: []model.StepResult{row}}
	block := s.observeLegacySteps(context.Background(), primary, map[string]legacyStepReplay{"first": {}}, config.RuntimeTuningConfig{})
	if block.State != "steps" || len(block.Steps) != 1 || block.Steps[0].Report != nil || block.Steps[0].State != "unavailable" {
		t.Fatalf("unexpected observations after native handoff failed: %+v", block)
	}
	snapshot := &legacyURLSnapshot{source: strings.NewReader("unfinished")}
	report, reason := s.observeLegacyStep(context.Background(), legacyStepReplay{url: snapshot}, config.RuntimeTuningConfig{})
	if report != nil || reason == "" {
		t.Fatalf("used partial URL input: %+v %q", report, reason)
	}
}
