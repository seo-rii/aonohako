package execute

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func scopedJudgeFixture(t *testing.T) (*model.RunRequest, config.Config) {
	t.Helper()
	req := judgeRequest()
	req.IgnoreTLE, req.WorkMeasurementLimits = false, nil
	req.ProblemID, req.Stdin, req.ExpectedStdout = "contest-1/a", "12\n", "24\n"
	req.Binaries = []model.Binary{{Name: "Main", Mode: "exec", DataB64: base64.StdEncoding.EncodeToString(cpuFeatureELFFixture())}}
	cfg := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{})
	measurement := &cfg.Execution.WorkMeasurement
	measurement.Judge = judgeMeasurementConfig().Judge
	measurement.CPUIDProfile = durante.CPUIDProfileX8664V2
	measurement.TimeModels.Profiles[0].CPUIDProfile = durante.CPUIDProfileX8664V2
	candidate, err := candidateJudgeWorkLimits(req, *measurement)
	if err != nil {
		t.Fatal(err)
	}
	clock := effectiveWorkClock(candidate, *measurement, config.DefaultRuntimeTuningConfig())
	if clock == nil {
		t.Fatal("missing fixture clock")
	}
	measurement.JudgeCases, err = durante.ParseJudgeCases(fmt.Sprintf(
		`{"schema":2,"cases":[{"problem_id":%q,"source_language":"CPP20","stdin_sha256":"%x","time_ms":%d,"wall_time_ms":%d,"memory_mb":%d,"model_binding":%q}]}`,
		req.ProblemID, sha256.Sum256([]byte(req.Stdin)), req.Limits.TimeMs, req.Limits.WallTimeMs, req.Limits.MemoryMB, clock.ModelBinding()))
	if err != nil {
		t.Fatal(err)
	}
	return req, cfg
}

func TestScopedJudgeAdmission(t *testing.T) {
	req, cfg := scopedJudgeFixture(t)
	if err := ApplyJudgeWorkLimits(req, cfg.Execution); err != nil {
		t.Fatal(err)
	}
	// A second submission has different instructions at its ELF entry point.
	second := append(cpuFeatureELFFixture(), 0xc3)
	second[0x1000] = 0x90
	binary.LittleEndian.PutUint64(second[96:], uint64(len(second)))
	binary.LittleEndian.PutUint64(second[104:], uint64(len(second)))
	req.Binaries[0].DataB64 = base64.StdEncoding.EncodeToString(second)
	if err := ApplyJudgeWorkLimits(req, cfg.Execution); err != nil {
		t.Fatalf("second static submission for the same case: %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*model.RunRequest)
	}{
		{"problem", func(r *model.RunRequest) { r.ProblemID += "2" }},
		{"stdin bytes", func(r *model.RunRequest) { r.Stdin = strings.TrimSpace(r.Stdin) }},
		{"time", func(r *model.RunRequest) { r.Limits.TimeMs++ }},
		{"nominal memory", func(r *model.RunRequest) { r.Limits.MemoryMB-- }},
		{"nominal wall", func(r *model.RunRequest) { r.Limits.WallTimeMs++ }},
		{"language", func(r *model.RunRequest) { r.Lang = "CPP17" }},
		{"entry mode", func(r *model.RunRequest) { r.EntryMode = "state" }},
		{"spj", func(r *model.RunRequest) { r.SPJ = &model.SPJSpec{} }},
		{"stdin URL", func(r *model.RunRequest) { r.StdinURL = "https://worker.invalid/input" }},
		{"expected URL", func(r *model.RunRequest) { r.ExpectedStdoutURL = "https://worker.invalid/output" }},
		{"binary URL", func(r *model.RunRequest) { r.Binaries[0].DataURL = "https://worker.invalid/program" }},
		{"extra file", func(r *model.RunRequest) { r.Binaries = append(r.Binaries, r.Binaries[0]) }},
		{"file outputs", func(r *model.RunRequest) { r.FileOutputs = []model.OutputFile{{Path: "answer"}} }},
		{"sidecars", func(r *model.RunRequest) { r.SidecarOutputs = []model.OutputFile{{Path: "extra"}} }},
		{"network", func(r *model.RunRequest) { r.EnableNetwork = true }},
		{"custom clock", func(r *model.RunRequest) { r.WorkMeasurementClock = &durante.WorkClockRequest{Version: 1, Budget: 1} }},
		{"forwarded model mismatch", func(r *model.RunRequest) { r.JudgeClockBinding = "work-clock-v1:sha256:" + strings.Repeat("f", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := *req
			r.Binaries = append([]model.Binary(nil), req.Binaries...)
			tc.edit(&r)
			if err := ApplyJudgeWorkLimits(&r, cfg.Execution); err == nil {
				t.Fatal("controller admitted an out-of-scope request")
			}
			result := NewWithConfig(cfg).Run(context.Background(), &r, Hooks{})
			if result.Status != model.RunStatusInitFail || result.WorkMeasurementJudgment != nil {
				t.Fatalf("worker executed an out-of-scope request: %+v", result)
			}
		})
	}
	newModel := cfg.Execution
	newModel.WorkMeasurement = snapshotWorkMeasurementConfig(newModel.WorkMeasurement)
	newModel.WorkMeasurement.TimeModels.Models[0].Coefficients.InstructionNSNumerator++
	if err := ApplyJudgeWorkLimits(req, newModel); err == nil || !strings.Contains(err.Error(), "no judge case") {
		t.Fatalf("changed model: %v", err)
	}
	newModel.WorkMeasurement.TimeModels = nil
	if err := ApplyJudgeWorkLimits(req, newModel); err == nil {
		t.Fatal("fell back to terminal judgment with explicit raw limits")
	}
}

func TestScopedJudgeForwardsWorkerRequirement(t *testing.T) {
	req, cfg := scopedJudgeFixture(t)
	workerCfg := cfg
	workerCfg.Execution.WorkMeasurement.JudgeCases = nil
	unscopedWorker := NewWithConfig(workerCfg)
	scopedWorker := NewWithConfig(cfg)
	calls := 0
	remote := &remoteRunner{executeURL: "http://worker.invalid/execute", workMeasurement: cfg.Execution.WorkMeasurement, uploadTimeout: time.Second}
	remote.client = &http.Client{Transport: executeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		var wire model.RunRequest
		if err := json.NewDecoder(request.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		if !wire.JudgeCaseRequired || wire.JudgeClockBinding == "" || wire.WorkMeasurementLimits == nil {
			t.Fatal("missing worker scope or effective clock")
		}
		if result := unscopedWorker.Run(context.Background(), &wire, Hooks{}); !strings.Contains(result.Reason, "judge_case_required") {
			t.Fatalf("worker without a registry: %+v", result)
		}
		wire.Stdin += "\n"
		if result := scopedWorker.Run(context.Background(), &wire, Hooks{}); !strings.Contains(result.Reason, "no judge case") {
			t.Fatalf("worker did not independently match case: %+v", result)
		}
		return nil, errors.New("captured forwarded request")
	})}
	remote.Run(context.Background(), req, Hooks{})
	req.ProblemID += "2"
	result := remote.Run(context.Background(), req, Hooks{})
	if calls != 1 || !strings.Contains(result.Reason, "no judge case") || req.JudgeCaseRequired {
		t.Fatalf("remote admission calls=%d result=%+v", calls, result)
	}
}

func TestScopedJudgeOtherModes(t *testing.T) {
	req, cfg := scopedJudgeFixture(t)
	req.Stdin = "unregistered input"
	legacy := cfg.Execution
	legacy.WorkMeasurement.JudgeCases = nil
	if err := ApplyJudgeWorkLimits(req, legacy); err != nil {
		t.Fatalf("legacy deployment: %v", err)
	}
	req.WorkMeasurementJudge = false
	for _, r := range []*model.RunRequest{req, {Lang: "CPP20"}, {WorkMeasurementShadow: true, WorkMeasurementLimits: req.WorkMeasurementLimits}} {
		if err := ApplyJudgeWorkLimits(r, cfg.Execution); err != nil {
			t.Fatalf("scope changed diagnostic or native admission: %v", err)
		}
	}
	peer, _ := peerTimeFixture(t, "interactive")
	if err := ApplyJudgeWorkLimits(peer, cfg.Execution); err != nil {
		t.Fatalf("terminal peer diagnostic: %v", err)
	}
	peer.WorkMeasurementPeerTime.Version = 2
	if err := ApplyJudgeWorkLimits(peer, cfg.Execution); err == nil {
		t.Fatal("controller allowed a deciding peer clock")
	}
	if result := NewWithConfig(cfg).Run(context.Background(), peer, Hooks{}); !strings.Contains(result.Reason, "deciding peer clocks") {
		t.Fatalf("worker allowed a deciding peer clock: %+v", result)
	}
}

func TestScopedJudgeExecutable(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"short", func(b []byte) []byte { return b[:30] }},
		{"script", func([]byte) []byte { return []byte("#!/bin/sh\n") }},
		{"shared object", func(b []byte) []byte { binary.LittleEndian.PutUint16(b[16:], uint16(elf.ET_DYN)); return b }},
		{"missing entry", func(b []byte) []byte { binary.LittleEndian.PutUint64(b[24:], 0); return b }},
		{"interpreter", func(b []byte) []byte {
			binary.LittleEndian.PutUint16(b[56:], 2)
			binary.LittleEndian.PutUint32(b[120:], uint32(elf.PT_INTERP))
			return b
		}},
		{"dynamic section", func(b []byte) []byte {
			binary.LittleEndian.PutUint16(b[56:], 2)
			binary.LittleEndian.PutUint32(b[120:], uint32(elf.PT_DYNAMIC))
			return b
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if staticJudgeExecutable(base64.StdEncoding.EncodeToString(tc.edit(cpuFeatureELFFixture()))) {
				t.Fatal("accepted unsupported file")
			}
		})
	}
}
