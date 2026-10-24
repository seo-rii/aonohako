package execute

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
	"aonohako/internal/timing"
)

func TestStdoutLimitVerdictPreservesExecutionFailures(t *testing.T) {
	for _, status := range []string{
		model.RunStatusInitFail, model.RunStatusTLE, model.RunStatusMLE,
		model.RunStatusWLE, model.RunStatusRE, model.RunStatusWA,
	} {
		t.Run(status, func(t *testing.T) {
			gotStatus, reason, source := applyStdoutLimitVerdict(status, "original failure", "original_source", true)
			if gotStatus != status || reason != "original failure" || source != "original_source" {
				t.Fatalf("existing failure was overwritten: (%q, %q, %q)", gotStatus, reason, source)
			}
		})
	}
	for _, status := range []string{"OK", model.RunStatusAccepted} {
		gotStatus, reason, source := applyStdoutLimitVerdict(status, "", "stdout", true)
		if gotStatus != model.RunStatusRE || reason != "stdout exceeded output limit" || source != "stdout_limit" {
			t.Fatalf("successful overflow = (%q, %q, %q)", gotStatus, reason, source)
		}
		gotStatus, reason, source = applyStdoutLimitVerdict(status, "", "stdout", false)
		if gotStatus != status || reason != "" || source != "stdout" {
			t.Fatalf("non-overflow verdict changed: (%q, %q, %q)", gotStatus, reason, source)
		}
	}
}

func TestJudgingAndInteractiveStdoutLimitPrecedence(t *testing.T) {
	exitZero, exitSeven := 0, 7
	req := &model.RunRequest{
		ExpectedStdout: "prefix",
		Limits:         model.Limits{TimeMs: 100, MemoryMB: 64, OutputBytes: len("prefix")},
	}
	cases := []struct {
		name       string
		res        execResult
		wantStatus string
		wantReason string
		wantSource string
	}{
		{"success", execResult{Status: "OK", ExitCode: &exitZero}, model.RunStatusRE, "stdout exceeded output limit", "stdout_limit"},
		{"initialization", execResult{Status: model.RunStatusInitFail, Reason: "start failed", VerdictSource: "sandbox_init"}, model.RunStatusInitFail, "start failed", "sandbox_init"},
		{"timeout", execResult{Status: model.RunStatusTLE, Reason: "wall time limit exceeded", VerdictSource: "wall_time"}, model.RunStatusTLE, "wall time limit exceeded", "wall_time"},
		{"memory", execResult{Status: model.RunStatusMLE, Reason: "memory limit exceeded", VerdictSource: "memory_cgroup"}, model.RunStatusMLE, "memory limit exceeded", "memory_cgroup"},
		{"workspace", execResult{Status: model.RunStatusWLE, Reason: "workspace quota exceeded", VerdictSource: "workspace_bytes"}, model.RunStatusWLE, "workspace quota exceeded", "workspace_bytes"},
		{"nonzero exit", execResult{Status: "OK", ExitCode: &exitSeven, Reason: "exit status 7"}, model.RunStatusRE, "exit status 7", "exit_code"},
		{"final cpu accounting", execResult{Status: "OK", ExitCode: &exitZero, CPUTimeMs: 101}, model.RunStatusTLE, "cpu time limit exceeded", "cpu_time_final"},
		{"reported memory", execResult{Status: "OK", ExitCode: &exitZero, MemoryKB: 65 * 1024}, model.RunStatusMLE, "", "memory_reported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.res
			res.Stdout = []byte("prefix")
			res.StdoutTruncated = true
			status, _, reason, source := evaluateRunStatus(context.Background(), Workspace{}, req, res, res.Stdout, "stdout", "", nil, config.DefaultRuntimeTuningConfig(), "", timing.CPUNormalizer{})
			if status != tc.wantStatus || reason != tc.wantReason || source != tc.wantSource {
				t.Fatalf("ordinary verdict = (%q, %q, %q), want (%q, %q, %q)", status, reason, source, tc.wantStatus, tc.wantReason, tc.wantSource)
			}
			interactive := interactiveResponse(req, req, res, execResult{Status: "OK", ExitCode: &exitZero}, 10, false, false)
			if interactive.Status != tc.wantStatus || interactive.VerdictSource != "contestant:"+tc.wantSource {
				t.Fatalf("interactive verdict = %+v, want status %q source contestant:%s", interactive, tc.wantStatus, tc.wantSource)
			}
			if tc.wantReason != "" && interactive.Reason != tc.wantReason {
				t.Fatalf("interactive reason = %q, want %q", interactive.Reason, tc.wantReason)
			}
			if interactive.Steps[0].Status != tc.wantStatus || interactive.Steps[0].VerdictSource != "contestant:"+tc.wantSource {
				t.Fatalf("contestant step disagrees with final verdict: %+v", interactive.Steps[0])
			}
		})
	}
}

func TestIgnoredTLEWithStdoutOverflowScoresZero(t *testing.T) {
	req := &model.RunRequest{ExpectedStdout: "prefix", IgnoreTLE: true, Limits: model.Limits{TimeMs: 100}}
	res := execResult{Status: model.RunStatusTLE, Reason: "wall time limit exceeded", VerdictSource: "wall_time", StdoutTruncated: true}
	status, score, reason, source := evaluateRunStatus(context.Background(), Workspace{}, req, res, []byte("prefix"), "stdout", "", nil, config.DefaultRuntimeTuningConfig(), "", timing.CPUNormalizer{})
	if status != model.RunStatusTLE || score == nil || *score != 0 || reason != res.Reason || source != res.VerdictSource {
		t.Fatalf("ignored-TLE overflow = (%q, %v, %q, %q)", status, score, reason, source)
	}
}

func TestFileJudgingIgnoresDiagnosticStdoutOverflow(t *testing.T) {
	req := &model.RunRequest{ExpectedStdout: "complete file", Limits: model.Limits{TimeMs: 100}}
	res := execResult{Status: "OK", StdoutTruncated: true}
	status, _, reason, source := evaluateRunStatus(context.Background(), Workspace{}, req, res, []byte(req.ExpectedStdout), "file_output", "", nil, config.DefaultRuntimeTuningConfig(), "", timing.CPUNormalizer{})
	if status != model.RunStatusAccepted || reason != "" || source != "file_output" {
		t.Fatalf("file-output verdict changed with diagnostic stdout overflow: (%q, %q, %q)", status, reason, source)
	}
}

func TestInteractiveCaptureTruncationDoesNotSelectOutputLimitVerdict(t *testing.T) {
	exitZero := 0
	captureLimit := 1
	req := &model.RunRequest{Limits: model.Limits{TimeMs: 100}, CaptureLimits: &model.CaptureLimits{StdoutBytes: &captureLimit}}
	resp := interactiveResponse(req, req, execResult{Status: "OK", ExitCode: &exitZero, Stdout: []byte("protocol")}, execResult{Status: "OK", ExitCode: &exitZero}, 10, false, false)
	if resp.Status != model.RunStatusAccepted || len(resp.Steps) != 2 || !resp.Steps[0].StdoutTruncated {
		t.Fatalf("display-only truncation changed interactive verdict: %+v", resp)
	}
}

func TestInteractorStdoutLimitPreservesExitCodeContract(t *testing.T) {
	req := &model.RunRequest{Limits: model.Limits{TimeMs: 100}}
	for _, tc := range []struct {
		exitCode   int
		wantStatus string
		wantSource string
	}{
		{0, model.RunStatusRE, "stdout_limit"},
		{1, model.RunStatusWA, "exit_code"},
		{3, model.RunStatusRE, "exit_code"},
	} {
		res := execResult{Status: "OK", ExitCode: &tc.exitCode, StdoutTruncated: true}
		status, _, source := classifyInteractorStatus(req, res)
		if status != tc.wantStatus || source != tc.wantSource {
			t.Fatalf("interactor exit %d = (%q, %q), want (%q, %q)", tc.exitCode, status, source, tc.wantStatus, tc.wantSource)
		}
	}
}

func TestStdoutLimitCombinationRequestsValidate(t *testing.T) {
	participant := model.RunProgram{ID: "participant", Lang: "binary", Binaries: []model.Binary{{Name: "runner", Mode: "exec", DataB64: b64("fixture")}}}
	interactor := model.RunProgram{ID: "judge", Lang: "binary", Binaries: []model.Binary{{Name: "judge", Mode: "exec", DataB64: b64("fixture")}}}
	limits := model.Limits{TimeMs: 1500, MemoryMB: 32, OutputBytes: 64}
	for _, mode := range []string{"ordinary", "legacy_first", "legacy_final", "pipeline_batch", "interactive", "pipeline_interactive"} {
		t.Run(mode, func(t *testing.T) {
			req, _, _ := stdoutLimitCombinationRequest(participant, interactor, limits, mode, 0)
			if err := runvalidation.Validate(req); err != nil {
				t.Fatalf("invalid regression fixture: %v", err)
			}
		})
	}
}

// The same contestant executable first overflows stdout, then exits normally,
// consumes CPU forever, exhausts address space, or exits nonzero. Running it
// through every supported single-contestant path catches wrapper overwrites
// that a verdict-helper unit test cannot detect.
func TestStdoutLimitCombinationsAcrossExecutionModes(t *testing.T) {
	requireSandboxSupport(t)
	participant := model.RunProgram{
		ID: "participant", Lang: "binary",
		Binaries: []model.Binary{{Name: "runner", Mode: "exec", DataB64: buildCTestBinary(t, `
#include <stdio.h>
#include <sys/mman.h>
#include <time.h>
#include <unistd.h>

int main(void) {
    int mode;
    if (scanf("%d", &mode) != 1) return 2;
    for (int i = 0; i < 512; ++i) putchar('x');
    fflush(stdout);
    if (mode == 1) for (;;) __asm__ volatile("");
    if (mode == 2) {
        for (int i = 0; i < 1024; ++i) {
            if (mmap(NULL, 1024 * 1024, PROT_NONE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0) == MAP_FAILED) {
                // Keep the exhausted address space observable to the parent
                // watchdog without relying on sandbox sleep syscall support.
                struct timespec start, now;
                if (clock_gettime(CLOCK_MONOTONIC, &start) != 0) return 10;
                do {
                    if (clock_gettime(CLOCK_MONOTONIC, &now) != 0) return 10;
                } while ((now.tv_sec - start.tv_sec) * 1000000000LL + now.tv_nsec - start.tv_nsec < 200000000LL);
                return 8;
            }
        }
        return 9;
    }
    if (mode == 3) return 7;
    return 0;
}
`)}}}
	interactor := model.RunProgram{
		ID: "judge", Lang: "binary",
		Binaries: []model.Binary{{Name: "judge", Mode: "exec", DataB64: buildCTestBinary(t, `
#include <stdio.h>

int main(int argc, char **argv) {
    if (argc != 4) return 3;
    FILE *input = fopen(argv[1], "r");
    int mode;
    if (!input || fscanf(input, "%d", &mode) != 1) return 3;
    fclose(input);
    printf("%d\n", mode);
    fflush(stdout);
    while (getchar() != EOF) {}
    return 0;
}
`)}}}
	limits := model.Limits{TimeMs: 1500, MemoryMB: 32, OutputBytes: 64}
	for _, outcome := range []struct {
		name       string
		mode       int
		wantStatus string
		wantSource string
	}{
		{"normal exit", 0, model.RunStatusRE, "stdout_limit"},
		{"time limit", 1, model.RunStatusTLE, ""},
		{"memory limit", 2, model.RunStatusMLE, ""},
		{"nonzero exit", 3, model.RunStatusRE, "exit_code"},
	} {
		for _, mode := range []string{"ordinary", "legacy_first", "legacy_final", "pipeline_batch", "interactive", "pipeline_interactive"} {
			t.Run(outcome.name+"/"+mode, func(t *testing.T) {
				req, wantPrefix, wantSteps := stdoutLimitCombinationRequest(participant, interactor, limits, mode, outcome.mode)
				if err := runvalidation.Validate(req); err != nil {
					t.Fatalf("invalid regression fixture: %v", err)
				}
				resp := New().Run(context.Background(), req, Hooks{})
				if resp.Status != outcome.wantStatus || !resp.StdoutTruncated {
					t.Fatalf("overflow combination = %+v, want %q and execution stdout truncation", resp, outcome.wantStatus)
				}
				sourceMatches := strings.HasPrefix(resp.VerdictSource, wantPrefix)
				if outcome.wantStatus == model.RunStatusTLE && mode == "interactive" {
					sourceMatches = sourceMatches || resp.VerdictSource == "interactive:wall_time"
				}
				if outcome.wantStatus == model.RunStatusTLE && mode == "pipeline_interactive" {
					sourceMatches = sourceMatches || resp.VerdictSource == "step:run:interactive:wall_time"
				}
				if !sourceMatches || outcome.wantSource != "" && resp.VerdictSource != wantPrefix+outcome.wantSource {
					t.Fatalf("source = %q, want prefix %q source %q", resp.VerdictSource, wantPrefix, outcome.wantSource)
				}
				if len(resp.Steps) != wantSteps {
					t.Fatalf("unexpected steps after failed execution: %+v", resp.Steps)
				}
				if (resp.Reason == "" && outcome.wantSource != "exit_code") || (outcome.wantStatus != model.RunStatusRE && strings.Contains(resp.Reason, "stdout")) {
					t.Fatalf("resource failure reason was lost or overwritten: %+v", resp)
				}
				for _, step := range resp.Steps {
					if step.ID == "first" && mode == "legacy_final" {
						continue
					}
					if step.ID != "interactor" && step.Status != resp.Status {
						t.Fatalf("step verdict disagrees with final result: %+v", step)
					}
				}
			})
		}
	}
}

func stdoutLimitCombinationRequest(participant, interactor model.RunProgram, limits model.Limits, mode string, outcome int) (*model.RunRequest, string, int) {
	input := fmt.Sprintf("%d\n", outcome)
	expected := strings.Repeat("x", limits.OutputBytes)
	switch mode {
	case "ordinary", "interactive":
		req := &model.RunRequest{Lang: participant.Lang, Binaries: participant.Binaries, Stdin: input, ExpectedStdout: expected, Limits: limits}
		if mode == "interactive" {
			req.Interactor = &model.InteractorSpec{Lang: interactor.Lang, Binaries: interactor.Binaries, Limits: &limits}
			return req, "contestant:", 2
		}
		return req, "", 0
	case "legacy_first", "legacy_final":
		setup := model.RunProgram{ID: "setup", Lang: "binary", Binaries: []model.Binary{{Name: "setup.sh", Mode: "exec", DataB64: b64(fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\n", input))}}}
		first, second := participant, setup
		if mode == "legacy_final" {
			first, second = setup, participant
		}
		req := &model.RunRequest{
			Programs: []model.RunProgram{first, second}, ExpectedStdout: expected,
			Steps: []model.RunStep{
				{ID: "first", ProgramID: first.ID, Stdin: input, Limits: limits, Handoff: &model.StepHandoff{ID: "handoff", From: "stdout", MaxBytes: int64(limits.OutputBytes)}},
				{ID: "second", ProgramID: second.ID, StdinFrom: "handoff", Limits: limits},
			},
		}
		if mode == "legacy_final" {
			return req, "step:second:", 2
		}
		return req, "step:first:", 1
	default:
		step := model.PipelineStep{ID: "run", Executor: model.PipelineExecutor{Kind: "batch", ProgramID: participant.ID}, Stdin: []model.PipelineRef{{Type: "resource", ID: "input"}}, Limits: limits}
		programs := []model.RunProgram{participant}
		prefix := "step:run:"
		if mode == "pipeline_interactive" {
			step.Executor = model.PipelineExecutor{Kind: "interactive", ParticipantProgramID: participant.ID, InteractorProgramID: interactor.ID, InteractorLimits: &limits}
			programs = append(programs, interactor)
			prefix += "contestant:"
		}
		return &model.RunRequest{Pipeline: &model.PipelineV1{
			Version:   1,
			Resources: map[string]model.PipelineResource{"input": {DataB64: b64(input)}, "expected": {DataB64: b64(expected)}},
			Programs:  programs, Steps: []model.PipelineStep{step},
			FinalJudge: model.PipelineFinalJudge{Kind: "diff", Input: model.PipelineRef{Type: "resource", ID: "input"}, Expected: model.PipelineRef{Type: "resource", ID: "expected"}, Actual: model.PipelineRef{Type: "step_stdout", StepID: "run"}},
		}}, prefix, 1
	}
}
