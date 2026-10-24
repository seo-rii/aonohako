package execute

import (
	"strings"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/remoteio"
	"aonohako/internal/runvalidation"
)

func remoteExecutionTimeout(req *model.RunRequest, uploadTimeout time.Duration) time.Duration {
	if uploadTimeout <= 0 {
		uploadTimeout = remoteio.DefaultRequestUploadTimeout
	}
	// Upload and queue/result transfer have independent bounded allowances.
	budget := uploadTimeout + remoteio.DefaultOperationOverhead
	addStage := func(timeMs int) {
		execution := time.Duration(min(max(timeMs, 1), runvalidation.MaxTimeMs)) * time.Millisecond
		budget += max(stdinURLDownloadTimeout, execution) + execution
	}
	needsDownload := strings.TrimSpace(req.StdinURL) != ""
	spj := req.SPJ
	if req.Pipeline != nil {
		needsDownload = false // URL resources have already been resolved by the API.
		for _, step := range req.Pipeline.Steps[:min(len(req.Pipeline.Steps), runvalidation.MaxPipelineSteps)] {
			wallMs := wallTimeLimitMs(step.Limits)
			interactive := strings.EqualFold(strings.TrimSpace(step.Executor.Kind), "interactive")
			if interactive {
				wallMs = interactiveWallTimeMs(step.Limits)
			}
			timeMs := max(step.Limits.TimeMs, wallMs)
			if interactive {
				// Mirror the role's runtime defaults, including inherited wall
				// budgets, before bounding the remote control plane.
				interactor := &model.RunRequest{
					Limits: step.Limits,
					Interactor: &model.InteractorSpec{Limits: step.Executor.InteractorLimits},
				}
				roleLimits := interactorRunRequest(interactor).Limits
				timeMs = max(timeMs, roleLimits.TimeMs, interactiveWallTimeMs(roleLimits))
			} else if step.Executor.InteractorLimits != nil {
				roleLimits := *step.Executor.InteractorLimits
				timeMs = max(timeMs, roleLimits.TimeMs, wallTimeLimitMs(roleLimits))
			}
			addStage(timeMs)
		}
		spj = req.Pipeline.FinalJudge.SPJ
	} else if runvalidation.UsesSteps(req) {
		needsDownload = false
		for _, step := range req.Steps[:min(len(req.Steps), runvalidation.MaxSteps)] {
			stepLimits := effectiveStepLimits(step)
			addStage(max(stepLimits.TimeMs, wallTimeLimitMs(stepLimits)))
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
		wallMs, _ := communicationWallAllowanceMs(platform.DeploymentTargetCloudRun, "", count, 1, communicationManagerLimits(limits).TimeMs)
		budget += stdinURLDownloadTimeout + time.Duration(wallMs)*time.Millisecond
	} else {
		timeMs := max(req.Limits.TimeMs, wallTimeLimitMs(req.Limits))
		if req.Interactor != nil {
			roleLimits := interactorRunRequest(req).Limits
			timeMs = max(req.Limits.TimeMs, interactiveWallTimeMs(req.Limits),
				roleLimits.TimeMs, interactiveWallTimeMs(roleLimits))
		}
		addStage(timeMs)
	}
	if needsDownload {
		// Legacy steps share this download allowance, rather than receiving a
		// fresh 60 seconds per URL. Keep it separate from sandbox preparation.
		budget += stdinURLDownloadTimeout
	}
	if spj != nil {
		timeMs := defaultSPJTimeMs
		if spj.Limits != nil {
			limits := *spj.Limits
			if limits.TimeMs <= 0 {
				limits.TimeMs = defaultSPJTimeMs
			}
			timeMs = max(limits.TimeMs, wallTimeLimitMs(limits))
		}
		addStage(timeMs)
	}
	return budget
}
