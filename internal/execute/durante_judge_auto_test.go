package execute

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func autoJudgeFixture(t *testing.T) (*model.RunRequest, config.Config) {
	t.Helper()
	req, cfg := scopedJudgeFixture(t)
	req.WorkMeasurement, req.WorkMeasurementTime, req.WorkMeasurementJudge = false, false, false
	cfg.Execution.WorkMeasurement.AutoJudge = true
	return req, cfg
}

func TestAutomaticJudgeSelection(t *testing.T) {
	req, cfg := autoJudgeFixture(t)
	cfg.Execution.WorkMeasurement.Observe = true
	if err := ApplyJudgeWorkLimits(req, cfg.Execution); err != nil {
		t.Fatal(err)
	}
	if !req.JudgeCaseRequired || !req.WorkMeasurementJudge || !req.WorkMeasurementTime || req.WorkMeasurementLimits == nil ||
		*req.WorkMeasurementLimits != cfg.Execution.WorkMeasurement.ResourcePolicy.MaxLimits ||
		effectiveWorkClock(req, cfg.Execution.WorkMeasurement, cfg.Execution.RuntimeTuning).ModelBinding() == "" ||
		automaticObservationRequested(req, cfg.Execution.WorkMeasurement) {
		t.Fatal("matching ordinary request did not select exactly the scoped deciding execution")
	}
	for _, tc := range []struct {
		name string
		edit func(*model.RunRequest, *durante.Config)
	}{
		{"default native", func(_ *model.RunRequest, c *durante.Config) { c.AutoJudge = false }},
		{"upstream native", func(r *model.RunRequest, _ *durante.Config) { r.SkipAutoJudge = true }},
		{"different problem", func(r *model.RunRequest, _ *durante.Config) { r.ProblemID += "-2" }},
		{"different stdin", func(r *model.RunRequest, _ *durante.Config) { r.Stdin += "\n" }},
		{"different deadline", func(r *model.RunRequest, _ *durante.Config) { r.Limits.TimeMs++ }},
		{"smaller nominal memory", func(r *model.RunRequest, _ *durante.Config) { r.Limits.MemoryMB = 1 }},
		{"shorter nominal wall", func(r *model.RunRequest, _ *durante.Config) { r.Limits.WallTimeMs = 1 }},
		{"larger nominal memory", func(r *model.RunRequest, c *durante.Config) {
			r.Limits.MemoryMB = c.ResourcePolicy.MaxLimits.MemoryMB + 1
		}},
		{"stale model", func(_ *model.RunRequest, c *durante.Config) {
			c.TimeModels.Models[0].Coefficients.InstructionNSNumerator++
		}},
		{"partial scoring", func(r *model.RunRequest, _ *durante.Config) { r.IgnoreTLE = true }},
		{"special judge", func(r *model.RunRequest, _ *durante.Config) { r.SPJ = &model.SPJSpec{} }},
		{"URL executable", func(r *model.RunRequest, _ *durante.Config) { r.Binaries[0].DataURL = "https://assets.invalid/program" }},
		{"script", func(r *model.RunRequest, _ *durante.Config) { r.Binaries[0].DataB64 = "YQ==" }},
		{"explicit measurement", func(r *model.RunRequest, _ *durante.Config) { r.WorkMeasurement = true }},
		{"explicit shadow", func(r *model.RunRequest, _ *durante.Config) { r.WorkMeasurementShadow = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, cfg := autoJudgeFixture(t)
			tc.edit(r, &cfg.Execution.WorkMeasurement)
			got := automaticJudgeRequest(r, cfg.Execution.WorkMeasurement, cfg.Execution.RuntimeTuning, cfg.Execution.RuntimeTuningProfiles)
			if got != r || r.WorkMeasurementJudge || r.WorkMeasurementLimits != nil {
				t.Fatal("altered explicit or nonmatching submission")
			}
		})
	}
}

func TestAutomaticJudgeDirect(t *testing.T) {
	req, cfg := autoJudgeFixture(t)
	t.Setenv("AONOHAKO_WORK_ROOT", t.TempDir())
	cfg.Execution.WorkMeasurement.Drrun = t.TempDir() + "/absent-drrun"
	got := NewWithConfig(cfg).Run(context.Background(), req, Hooks{})
	if got.Status != model.RunStatusInitFail || !strings.Contains(got.Reason, "durante preparation failed") ||
		got.WorkMeasurementJudgment != nil || got.WorkMeasurementExecution != nil || got.WorkMeasurement != nil ||
		got.Score != nil || got.WorkMeasurementObservation != nil || req.WorkMeasurementJudge {
		t.Fatalf("absent launcher should be an unscored worker failure: %+v", got)
	}
}

func TestAutomaticJudgeRemoteRouting(t *testing.T) {
	ordinary, cfg := autoJudgeFixture(t)
	for _, match := range []bool{true, false} {
		req := *ordinary
		if !match {
			req.Stdin += "\n"
		}
		calls := 0
		r := &remoteRunner{executeURL: "http://worker.invalid/execute", workMeasurement: cfg.Execution.WorkMeasurement, uploadTimeout: time.Second}
		r.client = &http.Client{Transport: executeRoundTripFunc(func(h *http.Request) (*http.Response, error) {
			calls++
			var wire model.RunRequest
			if err := json.NewDecoder(h.Body).Decode(&wire); err != nil {
				t.Fatal(err)
			}
			if match {
				if !wire.WorkMeasurementJudge || !wire.JudgeCaseRequired || wire.JudgeClockBinding == "" || h.Header.Get(NativeJudgeHeader) != "" {
					t.Fatal("implicit submission forwarded without independent worker scope and model checks")
				}
				worker := cfg.Execution
				worker.WorkMeasurement.AutoJudge = false
				if err := ApplyJudgeWorkLimits(&wire, worker); err != nil {
					t.Fatal(err)
				}
				wire.Limits.MemoryMB--
				if err := ApplyJudgeWorkLimits(&wire, worker); err == nil {
					t.Fatal("worker accepted limits outside the forwarded case")
				}
				wire.Limits.MemoryMB++
				wire.Stdin += "\n"
				if err := ApplyJudgeWorkLimits(&wire, worker); err == nil {
					t.Fatal("worker accepted input outside the forwarded case")
				}
			} else if wire.WorkMeasurement || wire.WorkMeasurementLimits != nil || h.Header.Get(NativeJudgeHeader) != "v1" {
				t.Fatal("native submission was not explicitly forwarded as native")
			}
			return nil, errors.New("captured request")
		})}
		r.Run(context.Background(), &req, Hooks{})
		if calls != 1 || req.WorkMeasurementJudge || req.WorkMeasurementLimits != nil {
			t.Fatalf("unexpected remote executions %d or caller mutation", calls)
		}
	}
}
