package execute

import (
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

// wallTimeLimitMs is the hard liveness guard, separate from the CPU accounting
// limit time_ms. Omitted wall_time_ms retains the previous wall=time behavior.
func wallTimeLimitMs(limits model.Limits) int {
	if limits.WallTimeMs > 0 {
		return min(limits.WallTimeMs, runvalidation.MaxTimeMs)
	}
	return min(max(1, limits.TimeMs), runvalidation.MaxTimeMs)
}

const (
	interactiveWallMultiplier = 3
	interactiveWallMinSlackMs = 3000
)

// interactiveWallTimeMs allows synchronous query/response sessions to wait
// without consuming their CPU budget. Explicit deadlines always take priority.
// The CPU limit itself is never multiplied. Clamp before arithmetic to avoid
// overflow for internal/unvalidated requests.
func interactiveWallTimeMs(limits model.Limits) int {
	if limits.WallTimeMs > 0 {
		return wallTimeLimitMs(limits)
	}
	cpuMs := int64(wallTimeLimitMs(limits))
	return int(min(int64(runvalidation.MaxTimeMs),
		max(cpuMs*interactiveWallMultiplier, cpuMs+interactiveWallMinSlackMs)))
}

// executionWallTimeMs combines a mode's default liveness allowance with the
// calibrated CPU guard. Explicit walls are never normalized or multiplied.
// Remote callers supply the maximum supported CPU guard; local callers supply
// their instance's calibrated guard. Clamp CPU input before either conversion.
func executionWallTimeMs(limits model.Limits, interactive bool, cpuWallLimit func(int) int) int {
	if limits.WallTimeMs > 0 {
		return wallTimeLimitMs(limits)
	}
	wallMs := wallTimeLimitMs(limits)
	if interactive {
		wallMs = interactiveWallTimeMs(limits)
	}
	cpuMs := min(max(1, limits.TimeMs), runvalidation.MaxTimeMs)
	return max(wallMs, cpuWallLimit(cpuMs))
}
