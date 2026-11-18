package execute

import (
	"context"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func judgeMeasurementConfig() durante.Config {
	cfg := timeMeasurementConfig()
	cfg.CPUIDProfile = durante.CPUIDProfileX8664V2
	policy := *cfg.TimePolicy
	policy.CPUIDProfile = durante.CPUIDProfileX8664V2
	cfg.TimePolicy = &policy
	cfg.Judge = durante.JudgePolicy{Enabled: true, VerdictsAllowed: true, Families: []string{"cpp"}, Integrity: durante.JudgeIntegrityOff}
	return cfg
}

func judgeRequest() *model.RunRequest {
	req := timeMeasurementRequest()
	req.WorkMeasurementJudge = true
	return req
}

// judgeResponse builds a finalized diagnostic+time response as the local path
// would, for a given raw status and work count, then is handed to the judge.
func judgeResponse(req *model.RunRequest, cfg durante.Config, status string, instructions uint64, output string) model.RunResponse {
	report := completeEnvelopeReport()
	report.Instructions = &instructions
	report.CPUIDProfile = cfg.CPUIDProfile
	exit := 0
	resp := model.RunResponse{
		Status: status, ExitCode: &exit, VerdictSource: output, WorkMeasurement: report,
		CPUTimeMs: 12, CPUTimeNs: 12_000_000, ProcessCPUTimeMs: 11, TimeMs: 20, WallTimeMs: 20, MemoryKB: 32000,
	}
	resp = finishWorkMeasurementDiagnostic(context.Background(), req, resp)
	return finishWorkMeasurementTime(req, resp, cfg)
}

func TestFinishJudgeVerdicts(t *testing.T) {
	cfg := judgeMeasurementConfig()
	// Model is 1 ns per instruction; nominal limit is 10 ms = 10,000,000 ns.
	for _, tc := range []struct {
		name         string
		status       string
		instructions uint64
		source       string
		wantStatus   string
		wantScore    bool
	}{
		{"accepted", model.RunStatusAccepted, 5_000_000, "stdout", model.RunStatusAccepted, true},
		{"tle on work", model.RunStatusAccepted, 20_000_000, "stdout", model.RunStatusTLE, false},
		{"wrong answer", model.RunStatusWA, 5_000_000, "stdout", model.RunStatusWA, false},
		{"raw cpu", model.RunStatusTLE, 5_000_000, "cpu_time", model.RunStatusInitFail, false},
		{"raw wall", model.RunStatusTLE, 5_000_000, "wall_time", model.RunStatusInitFail, false},
		{"raw rss", model.RunStatusMLE, 5_000_000, "memory", model.RunStatusInitFail, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := judgeRequest()
			resp := judgeResponse(req, cfg, tc.status, tc.instructions, tc.source)
			got := finishWorkMeasurementJudge(req, resp, cfg)
			if got.Status != tc.wantStatus || got.VerdictSource != "durante_judge" {
				t.Fatalf("status %q source %q, want %q (%s)", got.Status, got.VerdictSource, tc.wantStatus, got.Reason)
			}
			if (got.Score != nil) != tc.wantScore {
				t.Fatalf("score=%v want present=%v", got.Score, tc.wantScore)
			}
			if got.WorkMeasurementJudgment == nil || got.WorkMeasurementJudgment.Verdict != tc.wantStatus {
				t.Fatalf("missing/incorrect judgment: %+v", got.WorkMeasurementJudgment)
			}
			if tc.wantStatus == model.RunStatusInitFail {
				j := got.WorkMeasurementJudgment
				shadow := attachShadow(model.RunResponse{Status: model.RunStatusAccepted}, &got, "").WorkMeasurementShadow
				if j.State != "resource_exhausted" || j.EstimatedTimeNs != nil || j.TerminationSource != tc.source || shadow.Agreement != model.ShadowUnavailable {
					t.Fatalf("raw resource stop was scored: judgment=%+v shadow=%+v", j, shadow)
				}
				if err := validateRemoteWorkMeasurementJudge(req, got, cfg); err != nil {
					t.Fatalf("remote rejected the unscored resource stop: %v", err)
				}
				legacy := got
				legacy.Status = model.RunStatusTLE
				if err := validateRemoteWorkMeasurementJudge(req, legacy, cfg); err == nil {
					t.Fatal("remote accepted a raw resource stop as a virtual TLE")
				}
			}
			if got.WorkMeasurement == nil {
				t.Fatal("diagnostic report must be retained for audit")
			}
		})
	}
}

func TestFinishJudgeUnavailableIsRuntimeError(t *testing.T) {
	cfg := judgeMeasurementConfig()
	req := judgeRequest()
	// A signal-observed report converts to no estimate -> runtime error, not AC.
	resp := judgeResponse(req, cfg, model.RunStatusAccepted, 5_000_000, "stdout")
	resp.WorkMeasurement.SignalsObserved = 1
	resp.WorkMeasurement.Quality = "signals_observed"
	// Recompute time with the interrupted report.
	resp = finishWorkMeasurementTime(req, resp, cfg)
	got := finishWorkMeasurementJudge(req, resp, cfg)
	if got.Status != model.RunStatusRE || got.Score != nil {
		t.Fatalf("unconvertible observation must be RE: %q score=%v", got.Status, got.Score)
	}
	if got.WorkMeasurementJudgment.EstimatedTimeMs != nil {
		t.Fatal("no estimate should be reported for an unconvertible observation")
	}
}

func TestFinishJudgeRejectsMissingCPUIDReceipt(t *testing.T) {
	cfg := judgeMeasurementConfig()
	req := judgeRequest()
	host := cfg
	host.CPUIDProfile = "" // the observation's own receipt is empty
	resp := judgeResponse(req, host, model.RunStatusAccepted, 5_000_000, "stdout")
	resp = finishWorkMeasurementTime(req, resp, cfg)
	got := finishWorkMeasurementJudge(req, resp, cfg)
	if got.Status != model.RunStatusRE || got.Score != nil || got.WorkMeasurementJudgment.EstimatedTimeMs != nil {
		t.Fatalf("host-CPUID counts must not be judged with a profiled model: %q", got.Status)
	}
}

func TestFinishJudgeIgnoredWhenNotRequestedOrNotJudged(t *testing.T) {
	cfg := judgeMeasurementConfig()
	// Not requested: stays the diagnostic InitFail envelope.
	plain := timeMeasurementRequest()
	resp := judgeResponse(plain, cfg, model.RunStatusAccepted, 5_000_000, "stdout")
	if got := finishWorkMeasurementJudge(plain, resp, cfg); got.Status != model.RunStatusInitFail || got.WorkMeasurementJudgment != nil {
		t.Fatalf("unrequested judge must not change response: %q", got.Status)
	}
	// Requested but family not judged by the operator.
	noFamily := cfg
	noFamily.Judge.Families = []string{"cpython"}
	req := judgeRequest()
	resp = judgeResponse(req, noFamily, model.RunStatusAccepted, 5_000_000, "stdout")
	if got := finishWorkMeasurementJudge(req, resp, noFamily); got.Status != model.RunStatusInitFail || got.WorkMeasurementJudgment != nil {
		t.Fatalf("unjudged family must not get a verdict: %q", got.Status)
	}
}

func TestValidateJudgeRequest(t *testing.T) {
	cfg := judgeMeasurementConfig()
	if err := validateJudgeRequest(context.Background(), judgeRequest(), cfg); err != nil {
		t.Fatalf("valid judge request rejected: %v", err)
	}
	if err := validateJudgeRequest(context.Background(), timeMeasurementRequest(), cfg); err != nil {
		t.Fatalf("non-judge request must pass: %v", err)
	}
	// Judge disabled by the operator.
	disabled := cfg
	disabled.Judge = durante.JudgePolicy{Integrity: durante.JudgeIntegrityOff}
	if err := validateJudgeRequest(context.Background(), judgeRequest(), disabled); err == nil {
		t.Fatal("expected rejection when judge mode is disabled")
	}
	// Missing time flag.
	noTime := judgeRequest()
	noTime.WorkMeasurementTime = false
	if err := validateJudgeRequest(context.Background(), noTime, cfg); err == nil {
		t.Fatal("expected rejection without work_measurement_time")
	}
	// Shadow-only mode: a client cannot obtain a deciding verdict, but the
	// internal shadow execution can use the judge machinery.
	shadowOnly := cfg
	shadowOnly.Judge.VerdictsAllowed = false
	if err := validateJudgeRequest(context.Background(), judgeRequest(), shadowOnly); err == nil {
		t.Fatal("expected rejection of a verdict request in shadow-only mode")
	}
	if err := validateJudgeRequest(withShadowJudge(context.Background()), judgeRequest(), shadowOnly); err != nil {
		t.Fatalf("internal shadow judge rejected in shadow-only mode: %v", err)
	}
	// Family not judged.
	otherFamily := cfg
	otherFamily.Judge.Families = []string{"cpython"}
	if err := validateJudgeRequest(context.Background(), judgeRequest(), otherFamily); err == nil {
		t.Fatal("expected rejection for an unjudged family")
	}
}

func TestValidateRemoteJudgeRecompute(t *testing.T) {
	cfg := judgeMeasurementConfig()
	req := judgeRequest()
	resp := judgeResponse(req, cfg, model.RunStatusAccepted, 5_000_000, "stdout")
	resp = finishWorkMeasurementJudge(req, resp, cfg)
	// The control plane's own recomputation accepts a faithful result.
	if err := validateRemoteWorkMeasurement(req, resp); err != nil {
		t.Fatalf("faithful judge envelope rejected: %v", err)
	}
	if err := validateRemoteWorkMeasurementJudge(req, resp, cfg); err != nil {
		t.Fatalf("faithful judge verdict rejected: %v", err)
	}
	// A tampered verdict (claims AC while recomputation says TLE) is rejected.
	tampered := resp
	tle := model.RunStatusTLE
	judgment := *tampered.WorkMeasurementJudgment
	judgment.Verdict = tle
	tampered.WorkMeasurementJudgment = &judgment
	if err := validateRemoteWorkMeasurementJudge(req, tampered, cfg); err == nil {
		t.Fatal("expected rejection of a mismatched judgment")
	}
	// A runner that dropped the judgment block is rejected.
	missing := resp
	missing.WorkMeasurementJudgment = nil
	if err := validateRemoteWorkMeasurementJudge(req, missing, cfg); err == nil {
		t.Fatal("expected rejection of a missing judgment")
	}
	// An unsolicited judgment on a non-judge request is rejected.
	plain := timeMeasurementRequest()
	if err := validateRemoteWorkMeasurementJudge(plain, resp, cfg); err == nil {
		t.Fatal("expected rejection of an unsolicited judgment")
	}
	// A score inconsistent with the verdict is rejected.
	badScore := resp
	zero := 0.0
	badScore.Score = &zero
	badScore.Status = model.RunStatusWA
	wa := *badScore.WorkMeasurementJudgment
	wa.Verdict = model.RunStatusWA
	badScore.WorkMeasurementJudgment = &wa
	if err := validateRemoteWorkMeasurementJudge(req, badScore, cfg); err == nil {
		t.Fatal("expected rejection of a scored non-accepted verdict")
	}
}

func TestRemoteJudgeRequiresExecutionForStudentVerdict(t *testing.T) {
	cfg, req := judgeMeasurementConfig(), judgeRequest()
	for _, status := range []string{model.RunStatusAccepted, model.RunStatusTLE, model.RunStatusWA, model.RunStatusRE} {
		got := exchangeTimeMeasurementResult(t, req, model.RunResponse{Status: status}, cfg)
		if got.Status != model.RunStatusInitFail || !strings.Contains(got.Reason, "unverifiable work measurement judgment") {
			t.Errorf("unmeasured %s returned %+v", status, got)
		}
	}
	failure := model.RunResponse{Status: model.RunStatusInitFail, Reason: "admission failed"}
	if got := exchangeTimeMeasurementResult(t, req, failure, cfg); got.Status != failure.Status || got.Reason != failure.Reason {
		t.Errorf("legitimate pre-execution failure returned %+v", got)
	}
}

func TestJudgeConfigParsesFromEnv(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	_ = image
	policy, err := durante.ParseJudgePolicy("on", "cpp", "plausibility", "40")
	if err != nil {
		t.Fatal(err)
	}
	if !policy.JudgesFamily("cpp") || policy.JudgesFamily("cpython") {
		t.Fatalf("family gating wrong: %+v", policy)
	}
}
