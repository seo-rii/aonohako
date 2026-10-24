package execute

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

const shortCaseRepeatThresholdNs = uint64(100 * time.Millisecond)

// shortCaseRepeatEligible only admits ordinary, immutable batch requests. The
// caller must resolve URL payloads before checking eligibility. Replays must
// create fresh workspaces and suppress hooks; the first execution alone retains
// the original hook delivery behavior. A first execution that actually emits an
// image must be excluded by the caller, regardless of request eligibility.
func shortCaseRepeatEligible(req *model.RunRequest, _ Hooks) bool {
	if req == nil || req.SPJ != nil || req.Interactor != nil || req.Communication != nil || req.Pipeline != nil || runvalidation.UsesSteps(req) {
		return false
	}
	if req.EnableNetwork || req.IgnoreTLE || len(req.FileOutputs) > 0 || len(req.SidecarOutputs) > 0 {
		return false
	}
	if strings.TrimSpace(req.StdinURL) != "" || strings.TrimSpace(req.ExpectedStdoutURL) != "" {
		return false
	}
	for _, binary := range req.Binaries {
		if strings.TrimSpace(binary.DataURL) != "" {
			return false
		}
	}
	return true
}

// runShortCaseMedian repeats a short accepted execution twice, retaining the
// first observed failure instead of smoothing verdicts. The original execution
// has already completed; runAgain must execute the same immutable case in a new
// workspace, with unchanged limits and without externally visible hooks.
func runShortCaseMedian(ctx context.Context, first model.RunResponse, runAgain func(context.Context) model.RunResponse) model.RunResponse {
	if first.Status != model.RunStatusAccepted || first.CPUTimeMs < 0 || shortCaseCPUTimeNs(first) > shortCaseRepeatThresholdNs {
		return first
	}

	samples := []model.RunResponse{first}
	memoryKB, timeMs, wallTimeMs := first.MemoryKB, first.TimeMs, first.WallTimeMs
	finish := func(response model.RunResponse, selected int) model.RunResponse {
		response.MemoryKB = memoryKB
		response.TimeMs = timeMs
		response.WallTimeMs = wallTimeMs
		response.CPUTimeSampling = &model.CPUTimeSampling{
			Method:         "short-case-median-v1",
			SelectedSample: selected,
			Samples:        make([]model.CPUTimeSample, len(samples)),
		}
		for i, sample := range samples {
			response.CPUTimeSampling.Samples[i] = model.CPUTimeSample{
				Status:        sample.Status,
				CPUTimeMs:     sample.CPUTimeMs,
				CPUTimeNs:     sample.CPUTimeNs,
				RawCPUTimeMs:  sample.RawCPUTimeMs,
				RawCPUTimeNs:  sample.RawCPUTimeNs,
				WallTimeMs:    sample.WallTimeMs,
				MemoryKB:      sample.MemoryKB,
				CPUAccounting: sample.CPUAccounting,
			}
		}
		return response
	}
	canceled := func(err error) model.RunResponse {
		return finish(model.RunResponse{
			Status:        model.RunStatusInitFail,
			Reason:        "short-case repetition interrupted: " + err.Error(),
			VerdictSource: "short_case_context",
		}, 0)
	}
	for len(samples) < 3 {
		if err := ctx.Err(); err != nil {
			return canceled(err)
		}
		sample := runAgain(ctx)
		samples = append(samples, sample)
		memoryKB = max(memoryKB, sample.MemoryKB)
		timeMs += sample.TimeMs
		wallTimeMs += sample.WallTimeMs
		if sample.Status != model.RunStatusAccepted {
			return finish(sample, 0)
		}
	}
	if err := ctx.Err(); err != nil {
		return canceled(err)
	}

	indices := []int{0, 1, 2}
	sort.SliceStable(indices, func(i, j int) bool {
		return shortCaseCPUTimeNs(samples[indices[i]]) < shortCaseCPUTimeNs(samples[indices[j]])
	})
	selected := indices[1]
	median := samples[selected]
	response := first
	response.CPUTimeMs = median.CPUTimeMs
	response.CPUTimeNs = median.CPUTimeNs
	response.RawCPUTimeMs = median.RawCPUTimeMs
	response.RawCPUTimeNs = median.RawCPUTimeNs
	response.ProcessCPUTimeMs = median.ProcessCPUTimeMs
	response.CPUAccounting = median.CPUAccounting
	return finish(response, selected+1)
}

// The millisecond fallback supports responses from a legacy in-process runner;
// current sandbox executions retain nanosecond precision through selection.
func shortCaseCPUTimeNs(response model.RunResponse) uint64 {
	if response.CPUTimeNs > 0 || response.CPUTimeMs <= 0 {
		return response.CPUTimeNs
	}
	if uint64(response.CPUTimeMs) > math.MaxUint64/uint64(time.Millisecond) {
		return math.MaxUint64
	}
	return uint64(response.CPUTimeMs) * uint64(time.Millisecond)
}
