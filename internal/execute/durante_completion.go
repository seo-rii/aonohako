package execute

import (
	"context"

	"aonohako/internal/durante"
)

// The target has exited and its final resource checks have run. Freeze that
// completion state and stop its execution budget before parsing observations
// and hashing runtime artifacts: collector time must not invalidate an execution
// that already completed within its deadline.
func collectCompletedSandboxWorkMeasurement(ctx context.Context, stop context.CancelFunc, result execResult, collect func(int) *durante.Report) execResult {
	interrupted := ctx.Err() != nil
	stop()
	result.WorkMeasurement = collect(result.targetPID)
	discardIncompleteWorkMeasurement(interrupted, &result)
	return result
}
