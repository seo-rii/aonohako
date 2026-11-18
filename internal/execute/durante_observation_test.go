package execute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func observerConfig() durante.Config {
	cfg := timeMeasurementConfig()
	cfg.Observe = true
	cfg.TimePolicy, cfg.TimeModels = nil, nil
	cfg.CPUIDProfile = durante.CPUIDProfileX8664V2
	return cfg
}

func observerRequest() *model.RunRequest {
	req := envelopeRequest()
	req.WorkMeasurement, req.WorkMeasurementLimits, req.IgnoreTLE = false, nil, false
	return req
}

func TestAutomaticObservationSelection(t *testing.T) {
	cfg := observerConfig()
	if !automaticObservationRequested(observerRequest(), cfg) || automaticObservationRequested(nil, cfg) ||
		automaticObservationRequested(observerRequest(), durante.Config{}) {
		t.Fatal("automatic observation must require the operator setting and an ordinary request")
	}
	for name, change := range map[string]func(*model.RunRequest){
		"raw":    func(r *model.RunRequest) { r.WorkMeasurement = true },
		"limits": func(r *model.RunRequest) { r.WorkMeasurementLimits = &durante.ResourceLimits{} },
		"time":   func(r *model.RunRequest) { r.WorkMeasurementTime = true },
		"judge":  func(r *model.RunRequest) { r.WorkMeasurementJudge = true },
		"shadow": func(r *model.RunRequest) { r.WorkMeasurementShadow = true },
		"peer":   func(r *model.RunRequest) { r.WorkMeasurementPeerTime = &model.PeerTimeRequest{} },
	} {
		req := observerRequest()
		change(req)
		if automaticObservationRequested(req, cfg) {
			t.Errorf("explicit %s selected for automatic replay", name)
		}
	}
	for name, change := range map[string]func(*model.RunRequest){
		"network":          func(r *model.RunRequest) { r.EnableNetwork = true },
		"entry":            func(r *model.RunRequest) { r.EntryMode = "state" },
		"steps":            func(r *model.RunRequest) { r.Steps = []model.RunStep{{}} },
		"pipeline":         func(r *model.RunRequest) { r.Pipeline = &model.PipelineV1{} },
		"interactor":       func(r *model.RunRequest) { r.Interactor = &model.InteractorSpec{} },
		"communication":    func(r *model.RunRequest) { r.Communication = &model.CommunicationSpec{} },
		"sidecar":          func(r *model.RunRequest) { r.SidecarOutputs = []model.OutputFile{{Path: "result"}} },
		"binary_url":       func(r *model.RunRequest) { r.Binaries = []model.Binary{{DataURL: "unresolved"}} },
		"checker_url":      func(r *model.RunRequest) { r.SPJ = &model.SPJSpec{Binary: &model.Binary{DataURL: "unresolved"}} },
		"answer_url":       func(r *model.RunRequest) { r.ExpectedStdoutURL = "unresolved" },
		"ambiguous binary": func(r *model.RunRequest) { r.Lang = "binary" },
		"unqualified JVM":  func(r *model.RunRequest) { r.Lang = "JAVA" },
		"nominal over cap": func(r *model.RunRequest) { r.Limits.TimeMs = cfg.ResourcePolicy.MaxLimits.CPUTimeMs + 1 },
	} {
		req := observerRequest()
		change(req)
		if reason := observationEligible(req, cfg); reason == "" {
			t.Errorf("missing unavailable reason for %s", name)
		}
	}
}

func TestAutomaticObservationDoesNotReplayPythonCheckerOptions(t *testing.T) {
	req := observerRequest()
	req.SPJ = &model.SPJSpec{Lang: "PYTHON3"}
	req.PythonLibraryMode = "stdlib"
	limits := observerConfig().ResourcePolicy.MaxLimits
	for _, tc := range []struct{ language, want string }{{"CPP20", ""}, {"PYTHON3", "stdlib"}} {
		req.Lang = tc.language
		replay := observationRequest(req, limits)
		if replay.SPJ != nil || string(replay.PythonLibraryMode) != tc.want || req.PythonLibraryMode != "stdlib" {
			t.Fatalf("%s contestant received %q", tc.language, replay.PythonLibraryMode)
		}
	}
}

func TestExplicitShadowDoesNotAutomaticallyObserveItsPrimary(t *testing.T) {
	cfg := observerConfig()
	cfg.Judge.Enabled = false
	service := &Service{workMeasurement: cfg}
	got := service.Run(context.Background(), shadowRequest(), Hooks{})
	if got.WorkMeasurementShadow == nil || got.WorkMeasurementObservation != nil {
		t.Fatal("explicit shadow produced a second observation or lost its own receipt")
	}
}

func TestAutomaticObservationOnlyPublishesEquivalentContestantWork(t *testing.T) {
	for _, tc := range []struct {
		name, status, source                                         string
		checker, divergence, sampledChange, exhausted, invalidReport bool
		want                                                         string
	}{
		{name: "stdout AC", status: model.RunStatusAccepted, source: "stdout", want: "complete"},
		{name: "file WA", status: model.RunStatusWA, source: "file_output", want: "complete"},
		{name: "SPJ AC", status: model.RunStatusAccepted, source: "spj", checker: true, want: "complete"},
		{name: "SPJ WA", status: model.RunStatusWA, source: "spj", checker: true, want: "complete"},
		{name: "two different wrong answers", status: model.RunStatusWA, source: "stdout", divergence: true, want: "unavailable"},
		{name: "changing sampled bytes", status: model.RunStatusAccepted, source: "stdout", sampledChange: true, want: "unavailable"},
		{name: "raw TLE", status: model.RunStatusAccepted, source: "stdout", exhausted: true, want: "unavailable"},
		{name: "wrong profile", status: model.RunStatusAccepted, source: "stdout", invalidReport: true, want: "unavailable"},
		{name: "native wall time stop", status: model.RunStatusTLE, source: "wall_time", want: "unavailable"},
		{name: "WA without output verdict", status: model.RunStatusWA, source: "stdout_limit", want: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, req := observerConfig(), observerRequest()
			req.ExpectedStdout = "judge answer"
			if tc.checker {
				req.SPJ = &model.SPJSpec{Binary: &model.Binary{Name: "checker"}}
			}
			if tc.source == "file_output" {
				req.FileOutputs = []model.OutputFile{{Path: "result"}}
			}
			exit, raw, score := 0, uint64(19_000_000), 0.25
			primary := model.RunResponse{Status: tc.status, VerdictSource: tc.source, ExitCode: &exit, Stdout: "primary bytes", Score: &score,
				CPUTimeNs: 17_000_000, RawCPUTimeNs: &raw, MemoryKB: 4096, WallTimeMs: 21, CPUTimeSampling: &model.CPUTimeSampling{Method: "short-case-median-v1"}}
			report := completeEnvelopeReport()
			report.CPUIDProfile = cfg.CPUIDProfile
			var calls int
			service := Service{workMeasurement: cfg}
			got := service.runWithObservation(context.Background(), req, Hooks{OnLog: func(string, string) {}},
				func(ctx context.Context, stage *model.RunRequest, hooks Hooks) model.RunResponse {
					calls++
					capture := ctx.Value(observationOutputKey{}).(*observationOutput)
					capture.record([]byte("contestant bytes"))
					if calls == 1 {
						if stage != req || hooks.OnLog == nil {
							t.Fatal("primary request or hooks replaced")
						}
						if tc.sampledChange {
							capture.record([]byte("different accepted sample"))
						}
						return primary
					}
					if stage == req || stage.SPJ != nil || !stage.WorkMeasurement || stage.WorkMeasurementTime ||
						stage.WorkMeasurementJudge || *stage.WorkMeasurementLimits != cfg.ResourcePolicy.MaxLimits ||
						hooks.OnLog != nil || hooks.OnImage != nil || (tc.checker && stage.ExpectedStdout != "") {
						t.Fatal("wrong secondary request")
					}
					if tc.divergence {
						capture.record([]byte("different contestant bytes"))
					}
					if tc.invalidReport {
						report.CPUIDProfile = ""
					}
					state := "complete"
					if tc.exhausted {
						state = "resource_exhausted"
					}
					return model.RunResponse{WorkMeasurementExecution: &model.WorkMeasurementExecution{State: state}, WorkMeasurement: report}
				})
			obs := got.WorkMeasurementObservation
			if obs == nil || obs.State != tc.want || obs.Authority != durante.Authority || obs.Version != 1 ||
				(obs.Report == nil) != (tc.want == "unavailable") || (obs.Reason != "") != (tc.want == "unavailable") {
				t.Fatalf("wrong observation: %+v", obs)
			}
			if (obs.State == "complete" && obs.OutputCheck != "native_output") || (obs.State != "complete" && obs.OutputCheck != "") {
				t.Fatalf("unexpected output check %q", obs.OutputCheck)
			}
			wantCalls := 2
			if tc.sampledChange || observationOutputCheck(req, primary) == "" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("%d executions, want %d", calls, wantCalls)
			}
			got.WorkMeasurementObservation = nil
			if !reflect.DeepEqual(got, primary) {
				t.Fatalf("primary changed: %+v", got)
			}
		})
	}
}

func TestAutomaticObservationReusesNativeURLBytes(t *testing.T) {
	shortCaseTestWorkRoot(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) > 1 {
			t.Error("URL fetched twice")
		}
		_, _ = w.Write([]byte{0, 0xff, 10})
	}))
	defer server.Close()
	setStdinURLHTTPClientForTest(t, server.URL)
	cfg, req := observerConfig(), observerRequest()
	req.StdinURL = "http://payload.example/input"
	report := completeEnvelopeReport()
	report.CPUIDProfile = cfg.CPUIDProfile
	service := Service{workMeasurement: cfg}
	got := service.runWithObservation(context.Background(), req, Hooks{}, func(ctx context.Context, r *model.RunRequest, _ Hooks) model.RunResponse {
		input, err := ctx.Value(shadowStdinKey{}).(*stdinSnapshot).open(ctx, req.StdinURL, 4096)
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		data, err := io.ReadAll(input)
		if err != nil || string(data) != "\x00\xff\n" {
			t.Fatalf("input %x, %v", data, err)
		}
		ctx.Value(observationOutputKey{}).(*observationOutput).record(data)
		if r.WorkMeasurement {
			return model.RunResponse{WorkMeasurementExecution: &model.WorkMeasurementExecution{State: "complete"}, WorkMeasurement: report}
		}
		zero := 0
		return model.RunResponse{Status: model.RunStatusAccepted, VerdictSource: "stdout", ExitCode: &zero}
	})
	digest := sha256.Sum256([]byte{0, 0xff, 10})
	if calls.Load() != 1 || got.WorkMeasurementObservation.State != "complete" ||
		got.WorkMeasurementObservation.StdinSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("unbound URL replay: %+v", got.WorkMeasurementObservation)
	}
}

func TestAutomaticObservationCPULimitRequiresIndependentExpectedStdout(t *testing.T) {
	matches, wrong := true, false
	for _, tc := range []struct {
		name, primarySource, oracle, replaySource string
		match                                     *bool
		checker, file, attempt, complete          bool
	}{
		{name: "partial native output", primarySource: "cpu_time", oracle: "42\n", replaySource: "stdout", match: &matches, attempt: true, complete: true},
		{name: "cgroup polled", primarySource: "cpu_time_cgroup", oracle: "42", replaySource: "stdout", match: &matches, attempt: true, complete: true},
		{name: "final target CPU", primarySource: "cpu_time_final", oracle: "42", replaySource: "stdout", match: &matches, attempt: true, complete: true},
		{name: "final cgroup CPU", primarySource: "cpu_time_cgroup_final", oracle: "42", replaySource: "stdout", match: &matches, attempt: true, complete: true},
		{name: "wrong output", primarySource: "cpu_time", oracle: "42", replaySource: "stdout", match: &wrong, attempt: true},
		{name: "missing match", primarySource: "cpu_time", oracle: "42", replaySource: "stdout", attempt: true},
		{name: "different replay verdict source", primarySource: "cpu_time", oracle: "42", replaySource: "file_output", match: &matches, attempt: true},
		{name: "missing oracle", primarySource: "cpu_time"},
		{name: "empty after judge whitespace", primarySource: "cpu_time", oracle: " \r\t\n"},
		{name: "wall deadline", primarySource: "wall_time", oracle: "42"},
		{name: "unverified signal", primarySource: "cpu_rlimit", oracle: "42"},
		{name: "SPJ answer is not an oracle", primarySource: "cpu_time", oracle: "42", checker: true},
		{name: "file output has no stdout oracle", primarySource: "cpu_time", oracle: "42", file: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, req := observerConfig(), observerRequest()
			req.ExpectedStdout = tc.oracle
			if tc.checker {
				req.SPJ = &model.SPJSpec{}
			}
			if tc.file {
				req.FileOutputs = []model.OutputFile{{Path: "answer"}}
			}
			primary := model.RunResponse{Status: model.RunStatusTLE, VerdictSource: tc.primarySource, CPUTimeNs: 1_500_000}
			report := completeEnvelopeReport()
			report.CPUIDProfile = cfg.CPUIDProfile
			service := Service{workMeasurement: cfg}
			attempted := false
			got := service.runWithObservation(context.Background(), req, Hooks{}, func(ctx context.Context, stage *model.RunRequest, _ Hooks) model.RunResponse {
				if !stage.WorkMeasurement {
					// No complete native output or successful exit was observed.
					return primary
				}
				attempted = true
				if stage.ExpectedStdout != tc.oracle {
					t.Fatal("replaced the expected answer")
				}
				output := []byte("wrong answer")
				if tc.match != nil && *tc.match {
					output = []byte(tc.oracle + " \n")
				}
				ctx.Value(observationOutputKey{}).(*observationOutput).record(output)
				status := model.RunStatusWA
				if compareOutputs([]byte(stage.ExpectedStdout), output) {
					status = model.RunStatusAccepted
				}
				exit := 0
				measured := finishWorkMeasurementDiagnostic(ctx, stage, model.RunResponse{Status: status,
					VerdictSource: tc.replaySource, ExitCode: &exit, WorkMeasurement: report})
				if tc.match == nil {
					measured.WorkMeasurementExecution.OutputMatches = nil
				}
				return measured
			})
			obs := got.WorkMeasurementObservation
			if attempted != tc.attempt || (obs.State == "complete") != tc.complete || (obs.Report != nil) != tc.complete ||
				(tc.complete && obs.OutputCheck != "expected_output") || (!tc.complete && obs.OutputCheck != "") {
				t.Fatalf("attempted=%t observation=%+v", attempted, obs)
			}
			got.WorkMeasurementObservation = nil
			if !reflect.DeepEqual(got, primary) {
				t.Fatal("CPU-limited native result changed")
			}
		})
	}
}
