package execute

import (
	"context"
	"fmt"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/timing"
)

func (s *Service) runWorkMeasurementDiagnostic(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig) model.RunResponse {
	// Keep the caller's nominal limits and the shared service immutable. The
	// helper, runtime heap/AS settings, watchdog and final resource checks all
	// receive the same raw envelope, with no startup CPU normalization.
	rawReq := workMeasurementRawRequest(req)
	rawService := *s
	rawService.cpuNormalizer = timing.CPUNormalizer{}
	rawService.shortCaseSampling = false
	response := rawService.runOne(ctx, &rawReq, hooks, tuning, true).response
	return finishWorkMeasurementDiagnostic(ctx, req, response)
}

func workMeasurementRawRequest(req *model.RunRequest) model.RunRequest {
	raw := *req
	hard := *req.WorkMeasurementLimits
	raw.WorkMeasurementLimits = &hard
	raw.Limits.TimeMs = hard.CPUTimeMs
	raw.Limits.WallTimeMs = hard.WallTimeMs
	raw.Limits.MemoryMB = hard.MemoryMB
	// No partial-output scoring after a measurement resource abort.
	raw.IgnoreTLE = false
	return raw
}

func finishWorkMeasurementDiagnostic(ctx context.Context, req *model.RunRequest, response model.RunResponse) model.RunResponse {
	diagnostic := &model.WorkMeasurementExecution{
		Authority:         durante.Authority,
		State:             "failed",
		NominalLimits:     req.Limits,
		RawLimits:         *req.WorkMeasurementLimits,
		TerminationSource: response.VerdictSource,
	}
	switch {
	case ctx.Err() != nil:
		diagnostic.State = "cancelled"
		diagnostic.TerminationSource = "request_context"
	case response.Status == model.RunStatusTLE || response.Status == model.RunStatusMLE:
		diagnostic.State = "resource_exhausted"
	case response.Status == model.RunStatusAccepted || response.Status == model.RunStatusWA:
		if validDiagnosticWorkReport(req.Lang, response.WorkMeasurement) && ordinaryMeasurementOutputSource(response.VerdictSource) && response.ExitCode != nil && *response.ExitCode == 0 {
			diagnostic.State = "complete"
			matches := response.Status == model.RunStatusAccepted
			diagnostic.OutputMatches = &matches
		} else {
			diagnostic.State = "incomplete"
		}
	}
	response.WorkMeasurementExecution = diagnostic
	response.Score = nil
	response.CPUTimeNormalization = nil
	response.Status = model.RunStatusInitFail
	response.VerdictSource = "durante_" + diagnostic.State
	// Preserve a concrete underlying failure for diagnosis. Even complete work
	// counts are application-writable and must never confer a student AC/TLE.
	if diagnostic.State == "complete" {
		response.Reason = "work measurement completed; credited time and memory judgement are not enabled"
	} else {
		response.Reason = fmt.Sprintf("work measurement %s: %s", diagnostic.State, response.Reason)
	}
	return response
}

func ordinaryMeasurementOutputSource(source string) bool {
	return source == "stdout" || source == "file_output"
}

func validDiagnosticWorkReport(language string, report *durante.Report) bool {
	family, ok := durante.FamilyForLanguage(language)
	if !ok || durante.ValidateMeasurementFamily(family) != nil {
		return false
	}
	return report != nil && report.Schema == 1 && report.ClientVersion == 1 &&
		report.Complete && report.Reason == "" && report.Quality == "uninterrupted" && report.Authority == durante.Authority &&
		report.EngineProfile == durante.EngineProfile && report.Family == family &&
		(report.DispatchProfile == durante.DispatchDefault || (family == "cpp" && report.DispatchProfile == durante.DispatchGLIBC241 && report.HostLibc == "2.41")) &&
		report.Metric == durante.Metric && report.Scope == durante.Scope &&
		report.Instructions != nil && *report.Instructions > 0 && report.REPBytes != nil && report.REPEntries != nil &&
		(*report.REPEntries > 0 || *report.REPBytes == 0) &&
		report.UnsupportedREPEntries != nil && *report.UnsupportedREPEntries == 0 &&
		report.SignalsObserved == 0 && report.REPEnabled && report.Processes > 0 && report.Images >= report.Processes
}

func validateRemoteWorkMeasurement(req *model.RunRequest, response model.RunResponse) error {
	d := response.WorkMeasurementExecution
	if req.WorkMeasurementLimits == nil {
		if d != nil {
			return fmt.Errorf("invalid remote result: unsolicited work measurement execution")
		}
		return nil
	}
	if response.Status != model.RunStatusInitFail || response.Score != nil || response.CPUTimeNormalization != nil {
		return fmt.Errorf("invalid remote result: work measurement envelope cannot return a student verdict or normalized CPU")
	}
	if d == nil { // Pre-execution validation/initialization failure.
		return nil
	}
	if d.Authority != durante.Authority || d.RawLimits != *req.WorkMeasurementLimits || d.NominalLimits != req.Limits {
		return fmt.Errorf("invalid remote result: work measurement envelope mismatch")
	}
	if response.VerdictSource != "durante_"+d.State {
		return fmt.Errorf("invalid remote result: work measurement source mismatch")
	}
	switch d.State {
	case "complete":
		if d.OutputMatches == nil || !ordinaryMeasurementOutputSource(d.TerminationSource) || !validDiagnosticWorkReport(req.Lang, response.WorkMeasurement) || response.ExitCode == nil || *response.ExitCode != 0 {
			return fmt.Errorf("invalid remote result: incomplete work measurement marked complete")
		}
	case "failed", "incomplete", "cancelled", "resource_exhausted":
		if d.OutputMatches != nil {
			return fmt.Errorf("invalid remote result: failed work measurement claims output match")
		}
	default:
		return fmt.Errorf("invalid remote result: unknown work measurement state")
	}
	return nil
}
