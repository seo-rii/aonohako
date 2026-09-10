package execute

import (
	"context"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"aonohako/internal/model"
)

func shortCaseTestResponse(cpuNs uint64, memoryKB, wallMs int64) model.RunResponse {
	rawNs := cpuNs * 2
	rawMs := int64(rawNs / uint64(time.Millisecond))
	return model.RunResponse{
		Status:           model.RunStatusAccepted,
		CPUTimeMs:        int64(cpuNs / uint64(time.Millisecond)),
		CPUTimeNs:        cpuNs,
		RawCPUTimeMs:     &rawMs,
		RawCPUTimeNs:     &rawNs,
		ProcessCPUTimeMs: rawMs + 1,
		CPUAccounting:    &model.CPUAccounting{},
		TimeMs:           wallMs,
		WallTimeMs:       wallMs,
		MemoryKB:         memoryKB,
	}
}

func TestShortCaseRepeatEligible(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*model.RunRequest, *Hooks)
		want   bool
	}{
		{name: "ordinary batch", want: true},
		{name: "first-run log delivery", mutate: func(_ *model.RunRequest, h *Hooks) { h.OnLog = func(string, string) {} }, want: true},
		{name: "SPJ", mutate: func(r *model.RunRequest, _ *Hooks) { r.SPJ = &model.SPJSpec{} }},
		{name: "interactive", mutate: func(r *model.RunRequest, _ *Hooks) { r.Interactor = &model.InteractorSpec{} }},
		{name: "communication", mutate: func(r *model.RunRequest, _ *Hooks) { r.Communication = &model.CommunicationSpec{} }},
		{name: "pipeline", mutate: func(r *model.RunRequest, _ *Hooks) { r.Pipeline = &model.PipelineV1{} }},
		{name: "steps", mutate: func(r *model.RunRequest, _ *Hooks) { r.Steps = []model.RunStep{{}} }},
		{name: "programs", mutate: func(r *model.RunRequest, _ *Hooks) { r.Programs = []model.RunProgram{{}} }},
		{name: "network", mutate: func(r *model.RunRequest, _ *Hooks) { r.EnableNetwork = true }},
		{name: "ignore TLE", mutate: func(r *model.RunRequest, _ *Hooks) { r.IgnoreTLE = true }},
		{name: "file output", mutate: func(r *model.RunRequest, _ *Hooks) { r.FileOutputs = []model.OutputFile{{Path: "answer"}} }},
		{name: "sidecar output", mutate: func(r *model.RunRequest, _ *Hooks) { r.SidecarOutputs = []model.OutputFile{{Path: "image"}} }},
		{name: "unused image hook", mutate: func(_ *model.RunRequest, h *Hooks) { h.OnImage = func(string, string, int64) {} }, want: true},
		{name: "unresolved stdin", mutate: func(r *model.RunRequest, _ *Hooks) { r.StdinURL = "https://payload.example/input" }},
		{name: "unresolved answer", mutate: func(r *model.RunRequest, _ *Hooks) { r.ExpectedStdoutURL = "https://payload.example/answer" }},
		{name: "unresolved binary", mutate: func(r *model.RunRequest, _ *Hooks) { r.Binaries[0].DataURL = "https://payload.example/program" }},
	}
	if shortCaseRepeatEligible(nil, Hooks{}) {
		t.Fatal("nil request is eligible")
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &model.RunRequest{Binaries: []model.Binary{{Name: "Main", DataB64: "AA=="}}}
			hooks := Hooks{}
			if tt.mutate != nil {
				tt.mutate(req, &hooks)
			}
			if got := shortCaseRepeatEligible(req, hooks); got != tt.want {
				t.Fatalf("shortCaseRepeatEligible() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunShortCaseMedianPreservesPairedTimingAndFirstOutput(t *testing.T) {
	samples := []model.RunResponse{
		shortCaseTestResponse(7_900_000, 300, 10),
		shortCaseTestResponse(54_000_000, 500, 60),
		shortCaseTestResponse(8_100_000, 400, 20),
	}
	samples[0].Stdout = "original output"
	samples[0].Stderr = "original diagnostics"
	samples[0].StdoutTruncated = true
	samples[0].Reason = "original reason"
	samples[0].VerdictSource = "stdout"
	exitCode := 0
	score := 1.0
	samples[0].ExitCode = &exitCode
	samples[0].Score = &score
	samples[1].Stdout = "repeat output"
	samples[2].Stderr = "repeat diagnostics"
	ctx := context.Background()
	next := 1
	got := runShortCaseMedian(ctx, samples[0], func(gotCtx context.Context) model.RunResponse {
		if gotCtx != ctx {
			t.Fatal("replay received a different context")
		}
		result := samples[next]
		next++
		return result
	})
	if next != 3 || got.Status != model.RunStatusAccepted {
		t.Fatalf("executions = %d, status = %q", next, got.Status)
	}
	median := samples[2]
	if got.CPUTimeMs != median.CPUTimeMs || got.CPUTimeNs != median.CPUTimeNs || got.RawCPUTimeMs != median.RawCPUTimeMs || got.RawCPUTimeNs != median.RawCPUTimeNs || got.ProcessCPUTimeMs != median.ProcessCPUTimeMs || got.CPUAccounting != median.CPUAccounting {
		t.Fatalf("selected timing does not match one complete sample: got %+v, median %+v", got, median)
	}
	if got.MemoryKB != 500 || got.TimeMs != 90 || got.WallTimeMs != 90 {
		t.Fatalf("aggregate resources = memory %d, time %d, wall %d", got.MemoryKB, got.TimeMs, got.WallTimeMs)
	}
	if got.Stdout != samples[0].Stdout || got.Stderr != samples[0].Stderr || !got.StdoutTruncated || got.Reason != samples[0].Reason || got.VerdictSource != samples[0].VerdictSource || got.ExitCode != samples[0].ExitCode || got.Score != samples[0].Score {
		t.Fatalf("first output/verdict fields were not preserved: %+v", got)
	}
	if got.CPUTimeSampling == nil || got.CPUTimeSampling.Method != "short-case-median-v1" || got.CPUTimeSampling.SelectedSample != 3 || len(got.CPUTimeSampling.Samples) != 3 {
		t.Fatalf("sampling metadata = %+v", got.CPUTimeSampling)
	}
	for i, sample := range samples {
		metadata := got.CPUTimeSampling.Samples[i]
		if metadata.CPUTimeNs != sample.CPUTimeNs || metadata.RawCPUTimeNs != sample.RawCPUTimeNs || metadata.Status != sample.Status || metadata.MemoryKB != sample.MemoryKB || metadata.WallTimeMs != sample.WallTimeMs || metadata.CPUAccounting != sample.CPUAccounting {
			t.Fatalf("sample %d metadata mismatch: %+v", i+1, metadata)
		}
	}
	if samples[0].CPUTimeSampling != nil || samples[0].MemoryKB != 300 || samples[0].WallTimeMs != 10 {
		t.Fatal("input response was mutated")
	}
}

func TestRunShortCaseMedianSelectsNanosecondsAndStableTies(t *testing.T) {
	tests := []struct {
		name     string
		cpuTimes [3]uint64
		selected int
	}{
		{name: "submillisecond ordering", cpuTimes: [3]uint64{8_900_000, 8_100_000, 8_500_000}, selected: 3},
		{name: "first is median", cpuTimes: [3]uint64{8_500_000, 8_100_000, 8_900_000}, selected: 1},
		{name: "second is median", cpuTimes: [3]uint64{8_100_000, 8_500_000, 8_900_000}, selected: 2},
		{name: "stable ties", cpuTimes: [3]uint64{8_000_000, 8_000_000, 8_000_000}, selected: 2},
		{name: "slow accepted repeats still sampled", cpuTimes: [3]uint64{8_000_000, 110_000_000, 120_000_000}, selected: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := 1
			got := runShortCaseMedian(context.Background(), shortCaseTestResponse(tt.cpuTimes[0], 1, 1), func(context.Context) model.RunResponse {
				result := shortCaseTestResponse(tt.cpuTimes[next], 1, 1)
				next++
				return result
			})
			if next != 3 || got.CPUTimeSampling.SelectedSample != tt.selected || got.CPUTimeNs != tt.cpuTimes[tt.selected-1] {
				t.Fatalf("result = %+v, selected sample = %d", got, tt.selected)
			}
		})
	}
}

func TestRunShortCaseMedianThreshold(t *testing.T) {
	tests := []struct {
		name  string
		first model.RunResponse
		calls int
	}{
		{name: "zero", first: shortCaseTestResponse(0, 1, 1), calls: 2},
		{name: "at threshold", first: shortCaseTestResponse(100_000_000, 1, 1), calls: 2},
		{name: "one ns above", first: shortCaseTestResponse(100_000_001, 1, 1)},
		{name: "legacy at threshold", first: model.RunResponse{Status: model.RunStatusAccepted, CPUTimeMs: 100}, calls: 2},
		{name: "legacy above threshold", first: model.RunResponse{Status: model.RunStatusAccepted, CPUTimeMs: 101}},
		{name: "legacy negative", first: model.RunResponse{Status: model.RunStatusAccepted, CPUTimeMs: -1}},
		{name: "legacy overflow", first: model.RunResponse{Status: model.RunStatusAccepted, CPUTimeMs: math.MaxInt64}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			got := runShortCaseMedian(context.Background(), tt.first, func(context.Context) model.RunResponse {
				calls++
				return shortCaseTestResponse(1_000_000, 1, 1)
			})
			if calls != tt.calls {
				t.Fatalf("replay calls = %d, want %d", calls, tt.calls)
			}
			if calls == 0 && !reflect.DeepEqual(got, tt.first) {
				t.Fatalf("unsampled response changed: %+v", got)
			}
		})
	}
}

func TestRunShortCaseMedianDoesNotRetryFirstFailure(t *testing.T) {
	for _, status := range []string{model.RunStatusWA, model.RunStatusRE, model.RunStatusTLE, model.RunStatusMLE, model.RunStatusWLE, model.RunStatusInitFail, "OK"} {
		t.Run(status, func(t *testing.T) {
			first := shortCaseTestResponse(8_000_000, 1, 1)
			first.Status = status
			got := runShortCaseMedian(context.Background(), first, func(context.Context) model.RunResponse {
				t.Fatal("replayed a non-accepted first execution")
				return model.RunResponse{}
			})
			if !reflect.DeepEqual(got, first) {
				t.Fatalf("first failure changed: %+v", got)
			}
		})
	}
}

func TestRunShortCaseMedianNeverHidesRepeatedFailure(t *testing.T) {
	for _, status := range []string{model.RunStatusWA, model.RunStatusRE, model.RunStatusTLE, model.RunStatusMLE, model.RunStatusWLE, model.RunStatusInitFail} {
		for _, failureSample := range []int{2, 3} {
			t.Run(status+"/"+strconv.Itoa(failureSample), func(t *testing.T) {
				first := shortCaseTestResponse(8_000_000, 1000, 10)
				failed := shortCaseTestResponse(20_000_000, 500, 20)
				failed.Status = status
				failed.Reason = "repeat failed"
				failed.VerdictSource = "failure source"
				failed.Stdout = "failure output"
				next := 1
				got := runShortCaseMedian(context.Background(), first, func(context.Context) model.RunResponse {
					next++
					if next == failureSample {
						return failed
					}
					return first
				})
				if next != failureSample || got.Status != status || got.Reason != failed.Reason || got.VerdictSource != failed.VerdictSource || got.Stdout != failed.Stdout || got.RawCPUTimeNs != failed.RawCPUTimeNs {
					t.Fatalf("failure not preserved: samples %d, response %+v", next, got)
				}
				if got.MemoryKB != 1000 || got.WallTimeMs != int64((failureSample-1)*10+20) {
					t.Fatalf("failed repetition lost resource totals: %+v", got)
				}
				if got.CPUTimeSampling.SelectedSample != 0 || len(got.CPUTimeSampling.Samples) != failureSample {
					t.Fatalf("failed repetition claims median selection: %+v", got.CPUTimeSampling)
				}
			})
		}
	}
}

func TestRunShortCaseMedianContextCancellation(t *testing.T) {
	for _, cancelAfter := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(cancelAfter), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first := shortCaseTestResponse(8_000_000, 100, 10)
			completed := 1
			if cancelAfter == 1 {
				cancel()
			}
			got := runShortCaseMedian(ctx, first, func(context.Context) model.RunResponse {
				completed++
				if completed == cancelAfter {
					cancel()
				}
				return first
			})
			if completed != cancelAfter || got.Status != model.RunStatusInitFail || got.VerdictSource != "short_case_context" || !strings.Contains(got.Reason, context.Canceled.Error()) {
				t.Fatalf("cancellation was not preserved: completed %d, response %+v", completed, got)
			}
			if got.CPUTimeSampling.SelectedSample != 0 || len(got.CPUTimeSampling.Samples) != cancelAfter || got.WallTimeMs != int64(cancelAfter*10) {
				t.Fatalf("cancellation invented a sample: %+v", got)
			}
		})
	}
}
