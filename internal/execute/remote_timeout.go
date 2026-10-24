package execute

import (
	"strings"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/remoteio"
	"aonohako/internal/runvalidation"
	"aonohako/internal/timing"
)

func remoteExecutionTimeout(req *model.RunRequest, uploadTimeout time.Duration) time.Duration {
	if uploadTimeout <= 0 {
		uploadTimeout = remoteio.DefaultRequestUploadTimeout
	}
	// Upload and queue/result transfer have independent bounded allowances.
	budget := uploadTimeout + remoteio.DefaultOperationOverhead
	addStage := func(wallMs int) {
		// This is already an effective wall allowance. Scaling it here would
		// multiply explicit deadlines and interactive protocol slack again.
		execution := time.Duration(max(wallMs, 1)) * time.Millisecond
		budget += max(stdinURLDownloadTimeout, execution) + execution
	}
	needsDownload := strings.TrimSpace(req.StdinURL) != ""
	spj := req.SPJ
	if req.Pipeline != nil {
		needsDownload = false // URL resources have already been resolved by the API.
		for _, step := range req.Pipeline.Steps[:min(len(req.Pipeline.Steps), runvalidation.MaxPipelineSteps)] {
			interactive := strings.EqualFold(strings.TrimSpace(step.Executor.Kind), "interactive")
			wallMs := executionWallTimeMs(step.Limits, interactive, timing.MaximumCPUWallLimitMillis)
			if interactive {
				// Mirror the role's runtime defaults, including inherited wall
				// budgets, before bounding the remote control plane.
				interactor := &model.RunRequest{
					Limits:     step.Limits,
					Interactor: &model.InteractorSpec{Limits: step.Executor.InteractorLimits},
				}
				roleLimits := interactorRunRequest(interactor).Limits
				wallMs = max(wallMs, executionWallTimeMs(roleLimits, true, timing.MaximumCPUWallLimitMillis))
			} else if step.Executor.InteractorLimits != nil {
				roleLimits := *step.Executor.InteractorLimits
				wallMs = max(wallMs, executionWallTimeMs(roleLimits, false, timing.MaximumCPUWallLimitMillis))
			}
			addStage(wallMs)
		}
		spj = req.Pipeline.FinalJudge.SPJ
	} else if runvalidation.UsesSteps(req) {
		needsDownload = false
		for _, step := range req.Steps[:min(len(req.Steps), runvalidation.MaxSteps)] {
			stepLimits := effectiveStepLimits(step)
			addStage(executionWallTimeMs(stepLimits, false, timing.MaximumCPUWallLimitMillis))
			needsDownload = needsDownload || strings.TrimSpace(step.StdinURL) != ""
			for _, part := range step.StdinParts {
				needsDownload = needsDownload || strings.TrimSpace(part.DataURL) != ""
			}
		}
	} else if req.Communication != nil {
		limits := req.Limits
		limits.TimeMs = min(max(limits.TimeMs, 1), runvalidation.MaxTimeMs)
		count := min(max(req.Communication.ParticipantCount, runvalidation.MinCommunicationParticipants), runvalidation.MaxCommunicationParticipants)
		// The control plane does not know the remote CPU/cgroup shape. Cover
		// the longest supported wall allowance: one runnable target CPU, plus
		// the manager's longer limit and Cloud Run contention slack.
		managerTimeMs := timing.MaximumCPURawLimitMillis(communicationManagerLimits(limits).TimeMs)
		wallMs, _ := communicationWallAllowanceMs(platform.DeploymentTargetCloudRun, "", count, 1, managerTimeMs)
		budget += stdinURLDownloadTimeout + time.Duration(wallMs)*time.Millisecond
	} else {
		wallMs := executionWallTimeMs(req.Limits, req.Interactor != nil, timing.MaximumCPUWallLimitMillis)
		if req.Interactor != nil {
			roleLimits := interactorRunRequest(req).Limits
			wallMs = max(wallMs, executionWallTimeMs(roleLimits, true, timing.MaximumCPUWallLimitMillis))
		}
		addStage(wallMs)
		// Sampling is a downstream policy. Reserve two additional complete
		// runs for eligible batches; each replay creates its own sandbox.
		// URL resources are resolved/frozen before the downstream eligibility
		// check, so erase their references only from this private copy.
		repeatReq := *req
		repeatReq.StdinURL = ""
		repeatReq.ExpectedStdoutURL = ""
		repeatReq.Binaries = append([]model.Binary(nil), req.Binaries...)
		for i := range repeatReq.Binaries {
			repeatReq.Binaries[i].DataURL = ""
		}
		if shortCaseRepeatEligible(&repeatReq, Hooks{}) {
			addStage(wallMs)
			addStage(wallMs)
		}
	}
	if needsDownload {
		// Legacy steps share this download allowance, rather than receiving a
		// fresh 60 seconds per URL. Keep it separate from sandbox preparation.
		budget += stdinURLDownloadTimeout
	}
	if spj != nil {
		limits := model.Limits{TimeMs: defaultSPJTimeMs}
		if spj.Limits != nil {
			limits = *spj.Limits
			if limits.TimeMs <= 0 {
				limits.TimeMs = defaultSPJTimeMs
			}
		}
		addStage(executionWallTimeMs(limits, false, timing.MaximumCPUWallLimitMillis))
	}
	return budget
}
