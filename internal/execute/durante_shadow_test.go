package execute

import (
	"context"
	"strings"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

func shadowRequest() *model.RunRequest {
	req := envelopeRequest()
	req.WorkMeasurement = false
	req.IgnoreTLE = false
	req.WorkMeasurementShadow = true
	return req
}

func TestShadowRequestShape(t *testing.T) {
	if err := runvalidation.ValidateWorkMeasurementShadow(shadowRequest()); err != nil {
		t.Fatalf("valid shadow rejected: %v", err)
	}
	primary, shadow := runvalidation.ShadowRequests(shadowRequest())
	if primary.WorkMeasurementShadow || primary.WorkMeasurementLimits != nil || primary.WorkMeasurement || primary.WorkMeasurementJudge {
		t.Fatalf("primary request is not ordinary: %+v", primary)
	}
	if shadow.WorkMeasurementShadow || !shadow.WorkMeasurement || !shadow.WorkMeasurementTime || !shadow.WorkMeasurementJudge || shadow.WorkMeasurementLimits == nil {
		t.Fatalf("shadow request is not a judge envelope: %+v", shadow)
	}
	original := shadowRequest()
	_, copy := runvalidation.ShadowRequests(original)
	copy.WorkMeasurementLimits.CPUTimeMs = 1
	if original.WorkMeasurementLimits.CPUTimeMs == 1 {
		t.Fatal("shadow request shares the caller's envelope")
	}
	for name, mutate := range map[string]func(*model.RunRequest){
		"metered primary":    func(r *model.RunRequest) { r.WorkMeasurement = true },
		"time primary":       func(r *model.RunRequest) { r.WorkMeasurementTime = true },
		"judge primary":      func(r *model.RunRequest) { r.WorkMeasurementJudge = true },
		"missing envelope":   func(r *model.RunRequest) { r.WorkMeasurementLimits = nil },
		"spj":                func(r *model.RunRequest) { r.SPJ = &model.SPJSpec{} },
		"envelope too small": func(r *model.RunRequest) { r.WorkMeasurementLimits.CPUTimeMs = r.Limits.TimeMs - 1 },
		"generic binary":     func(r *model.RunRequest) { r.Lang = "binary" },
	} {
		req := shadowRequest()
		mutate(req)
		if err := runvalidation.ValidateWorkMeasurementShadow(req); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func shadowJudgeResponse(t *testing.T, cfg durante.Config, instructions uint64, status string) model.RunResponse {
	t.Helper()
	_, shadowReq := runvalidation.ShadowRequests(shadowRequest())
	resp := judgeResponse(&shadowReq, cfg, status, instructions, "stdout")
	resp.Stdout, resp.Stderr = "private output", "private error"
	return finishWorkMeasurementJudge(&shadowReq, resp, cfg)
}

func TestAttachShadowNeverChangesPrimary(t *testing.T) {
	cfg := judgeMeasurementConfig()
	score := 1.0
	primary := model.RunResponse{Status: model.RunStatusAccepted, Score: &score, CPUTimeMs: 7, CPUTimeNs: 7_000_000, MemoryKB: 900, VerdictSource: "stdout", Stdout: "visible"}
	for _, tc := range []struct {
		name         string
		instructions uint64
		agreement    string
		shadowStatus string
	}{
		{"agree", 5_000_000, model.ShadowAgree, model.RunStatusAccepted},
		{"disagree", 20_000_000, model.ShadowDisagree, model.RunStatusTLE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			shadow := shadowJudgeResponse(t, cfg, tc.instructions, model.RunStatusAccepted)
			got := attachShadow(primary, &shadow, "")
			if got.Status != primary.Status || got.Score != primary.Score || got.CPUTimeMs != 7 || got.MemoryKB != 900 || got.VerdictSource != "stdout" || got.Stdout != "visible" {
				t.Fatalf("primary changed: %+v", got)
			}
			b := got.WorkMeasurementShadow
			if b == nil || b.Version != 1 || b.Authority != durante.Authority || b.Agreement != tc.agreement || b.ShadowStatus != tc.shadowStatus || b.PrimaryStatus != model.RunStatusAccepted || b.PrimaryCPUTimeMs != 7 {
				t.Fatalf("shadow block: %+v", b)
			}
			if b.Response == nil || b.Response.Stdout != "" || b.Response.Stderr != "" || b.Response.WorkMeasurementJudgment == nil {
				t.Fatalf("shadow observation not trimmed or missing judgment: %+v", b.Response)
			}
		})
	}
	unavailable := attachShadow(primary, nil, "judge shadow mode is not enabled on this deployment")
	if b := unavailable.WorkMeasurementShadow; unavailable.Status != primary.Status || b.Agreement != model.ShadowUnavailable || b.Response != nil || b.Unavailable == "" {
		t.Fatalf("unavailable shadow: %+v", b)
	}
}

func TestShadowGate(t *testing.T) {
	cfg := judgeMeasurementConfig()
	cfg.Judge.VerdictsAllowed = false // shadow mode
	if reason := shadowGateUnavailable(shadowRequest(), cfg); reason != "" {
		t.Fatalf("shadow mode refused: %s", reason)
	}
	disabled := cfg
	disabled.Judge = durante.JudgePolicy{Integrity: durante.JudgeIntegrityOff}
	if shadowGateUnavailable(shadowRequest(), disabled) == "" {
		t.Fatal("disabled judge machinery ran a shadow")
	}
	other := cfg
	other.Judge.Families = []string{"cpython"}
	if shadowGateUnavailable(shadowRequest(), other) == "" {
		t.Fatal("unjudged family ran a shadow")
	}
}

func TestValidateRemoteShadow(t *testing.T) {
	cfg := judgeMeasurementConfig()
	req := shadowRequest()
	tuning := config.RuntimeTuningConfig{}
	primary := model.RunResponse{Status: model.RunStatusAccepted, CPUTimeMs: 7, VerdictSource: "stdout"}
	shadow := shadowJudgeResponse(t, cfg, 20_000_000, model.RunStatusAccepted)
	good := attachShadow(primary, &shadow, "")
	if err := validateRemoteShadow(req, good, cfg, tuning); err != nil {
		t.Fatalf("faithful shadow rejected: %v", err)
	}
	unavailable := attachShadow(primary, nil, "not enabled")
	if err := validateRemoteShadow(req, unavailable, cfg, tuning); err != nil {
		t.Fatalf("faithful unavailable shadow rejected: %v", err)
	}
	mutate := func(f func(*model.WorkMeasurementShadow)) model.RunResponse {
		copy := good
		block := *good.WorkMeasurementShadow
		inner := *block.Response
		block.Response = &inner
		f(&block)
		copy.WorkMeasurementShadow = &block
		return copy
	}
	for name, resp := range map[string]model.RunResponse{
		"agreement claim": mutate(func(b *model.WorkMeasurementShadow) { b.Agreement = model.ShadowAgree }),
		"shadow status":   mutate(func(b *model.WorkMeasurementShadow) { b.ShadowStatus = model.RunStatusAccepted }),
		"primary status":  mutate(func(b *model.WorkMeasurementShadow) { b.PrimaryStatus = model.RunStatusWA }),
		"untrimmed":       mutate(func(b *model.WorkMeasurementShadow) { b.Response.Stdout = "leak" }),
		"forged verdict": mutate(func(b *model.WorkMeasurementShadow) {
			b.Response.Status = model.RunStatusAccepted
			b.ShadowStatus = model.RunStatusAccepted
			b.Agreement = model.ShadowAgree
		}),
		"missing": func() model.RunResponse { c := good; c.WorkMeasurementShadow = nil; return c }(),
	} {
		if err := validateRemoteShadow(req, resp, cfg, tuning); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := validateRemoteShadow(envelopeRequest(), good, cfg, tuning); err == nil || !strings.Contains(err.Error(), "unsolicited") {
		t.Fatalf("unsolicited shadow accepted: %v", err)
	}
}

func TestShadowReservesRemoteBudget(t *testing.T) {
	req := shadowRequest()
	primary, _ := runvalidation.ShadowRequests(req)
	withShadow := remoteExecutionTimeout(req, time.Second)
	alone := remoteExecutionTimeout(&primary, time.Second)
	wall := time.Duration(req.WorkMeasurementLimits.WallTimeMs) * time.Millisecond
	if withShadow < alone+wall*2 {
		t.Fatalf("shadow budget %v does not add a raw-envelope stage to %v", withShadow, alone)
	}
}

func TestShadowSkipsWhenGateUnavailable(t *testing.T) {
	cfg := judgeMeasurementConfig()
	cfg.Judge = durante.JudgePolicy{Integrity: durante.JudgeIntegrityOff}
	svc := NewWithConfig(config.Config{Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	// An empty request fails primary admission without executing a sandbox;
	// the shadow is reported unavailable and the primary result is unchanged.
	req := shadowRequest()
	req.Binaries = nil
	got := svc.Run(context.Background(), req, Hooks{})
	if got.Status != model.RunStatusInitFail || got.WorkMeasurementShadow == nil || got.WorkMeasurementShadow.Agreement != model.ShadowUnavailable || got.WorkMeasurementShadow.PrimaryStatus != got.Status {
		t.Fatalf("unexpected shadow handling: %+v", got)
	}
}

func TestRemoteShadowRoundTripKeepsPrimary(t *testing.T) {
	cfg := judgeMeasurementConfig()
	cfg.Judge.VerdictsAllowed = false // control plane in shadow mode
	req := shadowRequest()
	exit := 0
	primary := model.RunResponse{Status: model.RunStatusAccepted, ExitCode: &exit, CPUTimeMs: 7, CPUTimeNs: 7_000_000, WallTimeMs: 9, TimeMs: 9, MemoryKB: 900, VerdictSource: "stdout"}
	shadow := shadowJudgeResponse(t, cfg, 20_000_000, model.RunStatusAccepted)
	local := attachShadow(primary, &shadow, "")
	got := exchangeTimeMeasurementResult(t, req, local, cfg)
	if got.Status != model.RunStatusAccepted || got.WorkMeasurementShadow == nil || got.WorkMeasurementShadow.Agreement != model.ShadowDisagree || got.WorkMeasurementShadow.ShadowStatus != model.RunStatusTLE {
		t.Fatalf("faithful remote shadow changed: %+v", got.WorkMeasurementShadow)
	}
	// An older runner that ignores the shadow keeps its validated primary; the
	// comparison becomes unavailable instead of failing the verdict.
	old := exchangeTimeMeasurementResult(t, req, primary, cfg)
	if old.Status != model.RunStatusAccepted || old.WorkMeasurementShadow == nil || old.WorkMeasurementShadow.Agreement != model.ShadowUnavailable || !strings.Contains(old.WorkMeasurementShadow.Unavailable, "no shadow result") {
		t.Fatalf("old runner handling: %+v", old)
	}
	// A forged shadow verdict is dropped, never trusted, and the primary stays.
	forged := local
	block := *local.WorkMeasurementShadow
	inner := *block.Response
	inner.Status, block.ShadowStatus, block.Agreement = model.RunStatusAccepted, model.RunStatusAccepted, model.ShadowAgree
	block.Response = &inner
	forged.WorkMeasurementShadow = &block
	dropped := exchangeTimeMeasurementResult(t, req, forged, cfg)
	if dropped.Status != model.RunStatusAccepted || dropped.WorkMeasurementShadow == nil || dropped.WorkMeasurementShadow.Agreement != model.ShadowUnavailable || dropped.WorkMeasurementShadow.Response != nil {
		t.Fatalf("forged shadow was not dropped: %+v", dropped.WorkMeasurementShadow)
	}
}
