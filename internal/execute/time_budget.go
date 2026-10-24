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
