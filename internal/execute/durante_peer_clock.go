package execute

import (
	"context"
	"errors"
	"fmt"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func (plan *peerTimePlan) enableClock(req, roleReq *model.RunRequest, cfg durante.Config, budget model.PeerTimeBudget) error {
	if cfg.TimeModels == nil || cfg.TimeModels.Schema != 2 || !cfg.Judge.VerdictsAllowed || cfg.Judge.Integrity != durante.JudgeIntegrityOff || !cfg.Judge.JudgesFamily(plan.policy.Family) || plan.policy.Formula() == durante.TimeMethodLatencyBoundREP {
		return fmt.Errorf("requires a schema-2 bound instruction/REP judge model and enabled family with integrity off")
	}
	wall := roleReq.Limits.TimeMs
	if req.Interactor != nil || plan.role.interactive {
		wall = interactiveWallTimeMs(roleReq.Limits)
	} else if req.SPJ != nil || stagedPeerClocksRequested(req) {
		wall = wallTimeLimitMs(roleReq.Limits)
	}
	plan.rawLimits = durante.ResourceLimits{CPUTimeMs: roleReq.Limits.TimeMs, WallTimeMs: wall, MemoryMB: roleReq.Limits.MemoryMB}
	if err := cfg.ResourcePolicy.ValidateRequest(plan.rawLimits); err != nil {
		return err
	}
	if plan.rawLimits.CPUTimeMs <= budget.TimeMs || plan.rawLimits.WallTimeMs <= budget.TimeMs {
		return fmt.Errorf("raw CPU and wall safety limits must exceed the virtual time budget")
	}
	plan.clock = &durante.WorkClockRequest{Version: 1, Budget: uint64(budget.TimeMs) * 1000000, TimePolicy: &plan.policy}
	if budget.ModelBinding != "" && budget.ModelBinding != plan.clock.ModelBinding() {
		return fmt.Errorf("model_binding does not match the worker's effective role model")
	}
	return nil
}

func applyPeerWorkClock(req *model.RunRequest, plan peerTimePlan) {
	if plan.clock != nil {
		req.WorkMeasurementClock = plan.clock
		req.Limits.WallTimeMs = plan.rawLimits.WallTimeMs
	}
}

var errPeerRequestCancelled = errors.New("peer work clock request cancelled")

func peerWorkClockCause(parent, roleCtx context.Context) error {
	if parent.Err() != nil {
		return errPeerRequestCancelled
	}
	return context.Cause(roleCtx)
}

func peerClockRawBreach(usage *model.PeerClockUsage, limits durante.ResourceLimits) bool {
	return usage == nil || usage.CPUTimeNs == nil || usage.WallTimeMs == nil || usage.MemoryKB == nil ||
		*usage.WallTimeMs < 0 || *usage.MemoryKB < 0 || *usage.CPUTimeNs > uint64(limits.CPUTimeMs)*1000000 ||
		*usage.WallTimeMs > int64(limits.WallTimeMs) || *usage.MemoryKB > int64(limits.MemoryMB)*1024
}

func peerClockGuest(role string) bool { return role == "participant" || role == "contestant" }

func finishPeerWorkClock(req *model.RunRequest, meter *durante.Session, res *execResult, cause error, role string) {
	plan := req.WorkMeasurementClock
	if plan == nil {
		return
	}
	res.workClockReceipt = plan.Receipt(nil)
	// The interactive executor's capture buffer enforces its hard stdout
	// limit. Response-display truncation happens later and is independent.
	outputExceeded := (role == "contestant" && len(req.FileOutputs) == 0 || role == "interactor") && res.StdoutTruncated
	res.peerClockRawSource = res.VerdictSource
	if res.CPUAccounting != nil {
		res.peerClockUsage = &model.PeerClockUsage{StdoutLimitExceeded: outputExceeded, CPUTimeNs: &res.CPUTimeNs, WallTimeMs: &res.WallTimeMs, MemoryKB: &res.MemoryKB}
	}
	if res.WorkMeasurement == nil && meter != nil && res.targetPID > 0 && res.VerdictSource != "durante_work_clock_cleanup" {
		res.WorkMeasurement = meter.Collect(res.targetPID)
	}
	fail := func(source, raw, reason string) {
		res.Status, res.VerdictSource, res.peerClockRawSource, res.Reason = model.RunStatusInitFail, source, raw, reason
		res.WorkMeasurement = res.WorkMeasurement.WithoutTotals(reason)
	}
	source := res.VerdictSource
	limits := durante.ResourceLimits{CPUTimeMs: req.Limits.TimeMs, WallTimeMs: req.Limits.WallTimeMs, MemoryMB: req.Limits.MemoryMB}
	if errors.Is(cause, errPeerRequestCancelled) {
		fail("durante_peer_clock_error", "request_context", "work clock request cancelled")
		return
	}
	peerCancelled := errors.Is(cause, context.Canceled) && (source == "wall_time" || source == "signal" || source == "" || source == "stream_io")
	if outputExceeded && (peerCancelled || (cause == nil && (source == "" || source == "durante_work_clock") &&
		res.workClockProgress != nil && plan.Exceeds(*res.workClockProgress))) {
		fail("durante_peer_clock_error", "stdout_limit", "interactive stdout limit conflicts with clock termination")
		return
	}
	if peerCancelled && (res.peerClockUsage == nil || !peerClockRawBreach(res.peerClockUsage, limits)) {
		fail("durante_peer_cancelled", "peer_context", "peer execution cancelled")
		return
	}
	if cause != nil || (res.Status != "OK" && !(res.Status == model.RunStatusTLE && source == "durante_work_clock")) || peerClockRawBreach(res.peerClockUsage, limits) {
		if source == "" {
			source = "raw_execution"
		}
		fail("durante_peer_clock_error", source, "peer work clock failed: "+source)
		return
	}
	report, p := res.WorkMeasurement, res.workClockProgress
	if !validWorkClockIdentity(req, report) || p == nil || !p.Valid() || report.DispatchProfile != plan.TimePolicy.DispatchProfile ||
		(report.DispatchProfile == durante.DispatchGLIBC241 && report.HostLibc != "2.41") {
		fail("durante_peer_clock_error", "clock_unavailable", "peer work clock or installation unavailable")
		return
	}
	if source == "durante_work_clock" {
		if res.ExitCode != nil || !plan.Exceeds(*p) {
			fail("durante_peer_clock_error", "clock_unavailable", "unverified peer work budget stop")
			return
		}
		res.WorkMeasurement = report.WithoutTotals("peer work clock stopped execution")
	} else {
		if res.ExitCode == nil || *res.ExitCode < 0 || !terminalWorkClockMatches(req, plan, report, p) {
			fail("durante_peer_clock_error", "clock_unavailable", "peer terminal counters do not match the work clock")
			return
		}
		if plan.Exceeds(*p) && !(peerClockGuest(role) && *res.ExitCode != 0) {
			res.Status, res.VerdictSource, res.Reason = model.RunStatusTLE, "durante_work_clock_final", "peer work budget exceeded at exit"
		}
	}
	res.workClockReceipt = plan.Receipt(p)
}

// Build the public time result from owner evidence, never from weights returned
// by another runner. Both sides apply the same strict boundary to each role.
func peerClockResult(plan peerTimePlan, observed model.WorkMeasurementProcess) (*model.PeerTimeBudgetResult, error) {
	input := observed.TimeBudget
	result := &model.PeerTimeBudgetResult{Version: 2, Authority: durante.Authority, BudgetMs: plan.budgetMs, RawLimits: &plan.rawLimits}
	if input == nil {
		if observed.ExecutionStatus != model.RunStatusInitFail || observed.Report != nil || observed.ExitCode != nil {
			return nil, fmt.Errorf("missing role clock evidence")
		}
		result.Clock = plan.clock.Receipt(nil)
		result.RawSource = observed.TerminationSource
		result.Unavailable = "role_not_started"
		return result, nil
	}
	result.Clock, result.RawUsage, result.RawSource = input.Clock, input.RawUsage, input.RawSource
	c := result.Clock
	if plan.clock.TimePolicy == nil || c == nil || c.Version != 1 || c.Metric != plan.clock.MetricName() || c.Budget != plan.clock.Budget || c.ModelBinding != plan.clock.ModelBinding() {
		return nil, fmt.Errorf("role clock model or budget mismatch")
	}
	if c.Progress == nil {
		if c.State != "unavailable" || observed.ExecutionStatus != model.RunStatusInitFail || observed.Report != nil && !observed.Report.ValidUnavailableResult() {
			return nil, fmt.Errorf("inconsistent unavailable role clock")
		}
		if result.RawUsage == nil && observed.Report == nil && observed.ExitCode == nil && observed.TerminationSource != "durante_peer_cancelled" && observed.TerminationSource != "durante_peer_clock_error" {
			result.Unavailable = "role_not_started"
			return result, nil
		}
		switch observed.TerminationSource {
		case "durante_peer_clock_error":
			if result.RawSource == "" {
				return nil, fmt.Errorf("missing peer clock failure reason")
			}
			result.Unavailable = "clock_or_raw_execution_failed"
		case "durante_peer_cancelled":
			if result.RawSource != "peer_context" || (result.RawUsage != nil && (result.RawUsage.StdoutLimitExceeded || peerClockRawBreach(result.RawUsage, plan.rawLimits))) {
				return nil, fmt.Errorf("invalid sibling cancellation")
			}
			result.Unavailable = "peer_cancelled"
		default:
			return nil, fmt.Errorf("unexpected unavailable role clock cause")
		}
		return result, nil
	}
	report, p := observed.Report, c.Progress
	req := &model.RunRequest{Lang: plan.role.language}
	if c.State != "observed" || !p.Valid() || peerClockRawBreach(result.RawUsage, plan.rawLimits) ||
		!validWorkClockIdentity(req, report) || report.DispatchProfile != plan.policy.DispatchProfile ||
		(report.DispatchProfile == durante.DispatchGLIBC241 && report.HostLibc != "2.41") {
		return nil, fmt.Errorf("incompatible role clock evidence")
	}
	exceeded := plan.clock.Exceeds(*p)
	if result.RawUsage.StdoutLimitExceeded && (exceeded || plan.role.role != "contestant" && plan.role.role != "interactor") {
		return nil, fmt.Errorf("work clock conflicts with raw stdout limit")
	}
	result.Exceeded = &exceeded
	if observed.TerminationSource == "durante_work_clock" {
		if !exceeded || !report.ValidUnavailableResult() || observed.ExitCode != nil || observed.ExecutionStatus != model.RunStatusTLE || result.RawSource != "durante_work_clock" {
			return nil, fmt.Errorf("unverified live role budget cutoff")
		}
		return result, nil
	}
	if observed.ExitCode == nil || *observed.ExitCode < 0 || result.RawSource != "" {
		return nil, fmt.Errorf("missing terminal role clock evidence")
	}
	estimate, valid := terminalWorkClockEstimate(req, plan.clock, report, p, plan.runtimeID)
	if !valid {
		return nil, fmt.Errorf("invalid terminal role clock evidence")
	}
	result.Estimate = estimate
	if estimate == nil {
		result.Unavailable = durante.TimeNumericOverflow
	}
	if exceeded && !(peerClockGuest(plan.role.role) && *observed.ExitCode != 0) {
		if observed.ExecutionStatus != model.RunStatusTLE || observed.TerminationSource != "durante_work_clock_final" {
			return nil, fmt.Errorf("missing terminal role cutoff")
		}
	} else if observed.ExecutionStatus != "OK" || observed.TerminationSource != "" {
		return nil, fmt.Errorf("terminal role clock disagrees with status")
	}
	return result, nil
}

func forwardedPeerClockRequest(req *model.RunRequest, plans map[peerTimeIdentity]peerTimePlan) *model.RunRequest {
	if req.WorkMeasurementPeerTime == nil || req.WorkMeasurementPeerTime.Version != 2 {
		return req
	}
	out := *req
	peer := *req.WorkMeasurementPeerTime
	peer.Budgets = append([]model.PeerTimeBudget(nil), peer.Budgets...)
	for i, budget := range peer.Budgets {
		plan := plans[peerTimeKey(budget.Role, budget.ProgramID, budget.ParticipantIndex, budget.StepID)]
		peer.Budgets[i].ModelBinding = plan.clock.ModelBinding()
	}
	out.WorkMeasurementPeerTime = &peer
	return &out
}

// Communication also exposes the independently summed per-role CPU ledger.
// A deciding clock response must agree with that ledger for each seat.
func validatePeerClockCPU(response model.RunResponse) error {
	if len(response.WorkMeasurements) == 0 || len(response.CommunicationCPUByRole) != len(response.WorkMeasurements) {
		return fmt.Errorf("invalid remote result: missing peer clock CPU accounting")
	}
	for i, role := range response.WorkMeasurements {
		row := response.CommunicationCPUByRole[i]
		if role.TimeBudget == nil || (role.TimeBudget.RawUsage == nil) != (row.CPUTimeNs == nil) ||
			(role.TimeBudget.RawUsage != nil && !equalPipelineCounter(role.TimeBudget.RawUsage.CPUTimeNs, row.CPUTimeNs)) {
			return fmt.Errorf("invalid remote result: peer clock CPU usage disagrees with role accounting")
		}
	}
	return nil
}
