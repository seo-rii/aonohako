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
	addStage := func(timeMs int) {
		// The remote host's calibration is not known until its result arrives.
		// Bound public CPU time before scaling, then reserve both initialization
		// and execution for the slowest supported host.
		timeMs = min(max(timeMs, 1), runvalidation.MaxTimeMs)
		execution := time.Duration(timing.MaximumCPUWallLimitMillis(timeMs)) * time.Millisecond
		budget += max(stdinURLDownloadTimeout, execution) + execution
	}
	needsDownload := strings.TrimSpace(req.StdinURL) != ""
	spj := req.SPJ
	if req.Pipeline != nil {
		needsDownload = false // URL resources have already been resolved by the API.
		for _, step := range req.Pipeline.Steps[:min(len(req.Pipeline.Steps), runvalidation.MaxPipelineSteps)] {
			timeMs := step.Limits.TimeMs
			if step.Executor.InteractorLimits != nil {
				timeMs = max(timeMs, step.Executor.InteractorLimits.TimeMs)
			}
			addStage(timeMs)
		}
		spj = req.Pipeline.FinalJudge.SPJ
	} else if runvalidation.UsesSteps(req) {
		needsDownload = false
		for _, step := range req.Steps[:min(len(req.Steps), runvalidation.MaxSteps)] {
			addStage(effectiveStepLimits(step).TimeMs)
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
		timeMs := req.Limits.TimeMs
		if req.Interactor != nil {
			timeMs = max(timeMs, interactorRunRequest(req).Limits.TimeMs)
		}
		addStage(timeMs)
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
			addStage(timeMs)
			addStage(timeMs)
		}
	}
	if needsDownload {
		// Legacy steps share this download allowance, rather than receiving a
		// fresh 60 seconds per URL. Keep it separate from sandbox preparation.
		budget += stdinURLDownloadTimeout
	}
	if spj != nil {
		timeMs := defaultSPJTimeMs
		if spj.Limits != nil && spj.Limits.TimeMs > 0 {
			timeMs = spj.Limits.TimeMs
		}
		addStage(timeMs)
	}
	return budget
}
