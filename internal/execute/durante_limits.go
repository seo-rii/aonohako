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

// A normal nonzero application exit is still OK at the sandbox layer: the
// ordinary judge assigns its exit-code RE later. Any failure already present
// here is an execution/infrastructure failure and cannot retain work totals.
func discardIncompleteWorkMeasurement(ctx context.Context, result *execResult) {
	if ctx.Err() != nil || result.Status != "OK" || result.ExitCode == nil || *result.ExitCode < 0 {
		result.WorkMeasurement = result.WorkMeasurement.WithoutTotals("measurement execution did not complete normally")
	}
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
	if diagnostic.State != "complete" {
		response.WorkMeasurement = response.WorkMeasurement.WithoutTotals("diagnostic execution " + diagnostic.State)
	}
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
		report.Complete && report.Reason == "" && report.Authority == durante.Authority &&
		report.ValidDiagnosticContract() && report.ValidREPTelemetry() && report.ValidInstallationIdentity() &&
		((report.SignalsObserved == 0 && report.Quality == "uninterrupted") || (report.SignalsObserved > 0 && report.Quality == "signals_observed")) &&
		report.EngineProfile == durante.EngineProfile && report.Family == family &&
		(report.DispatchProfile == durante.DispatchDefault || (family == "cpp" && report.DispatchProfile == durante.DispatchGLIBC241 && report.HostLibc == "2.41")) &&
		report.Metric == durante.Metric && report.Scope == durante.Scope &&
		report.Instructions != nil && *report.Instructions > 0 && report.REPBytes != nil && report.REPEntries != nil &&
		(*report.REPEntries > 0 || *report.REPBytes == 0) &&
		report.UnsupportedREPEntries != nil && report.REPEnabled && report.Processes > 0 && report.Images >= report.Processes
}

func validateRemoteWorkMeasurement(req *model.RunRequest, response model.RunResponse) error {
	if err := validateRemotePeerWorkMeasurements(req, response); err != nil {
		return err
	}
	d := response.WorkMeasurementExecution
	if report := response.WorkMeasurement; report != nil {
		if (!req.WorkMeasurement && req.WorkMeasurementLimits == nil) || report.Schema != 1 || report.ClientVersion != 1 || !report.ValidDiagnosticContract() || report.Authority != durante.Authority || report.Metric != durante.Metric || report.Scope != durante.Scope {
			return fmt.Errorf("invalid remote result: absent or incompatible diagnostic contract")
		}
		if (report.Complete && !validDiagnosticWorkReport(req.Lang, report)) || (!report.Complete && !report.ValidUnavailableResult()) {
			return fmt.Errorf("invalid remote result: inconsistent diagnostic observation")
		}
		if report.Complete && (response.Status == model.RunStatusTLE || response.Status == model.RunStatusMLE || response.Status == model.RunStatusWLE) {
			return fmt.Errorf("invalid remote result: resource stop returned complete diagnostic totals")
		}
		if report.Complete && response.Status == model.RunStatusRE {
			switch response.VerdictSource {
			case "pids_cgroup", "pids_cgroup_final", "stream_io", "wait_status":
				return fmt.Errorf("invalid remote result: incomplete execution returned complete diagnostic totals")
			}
		}
	}
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
		if d.OutputMatches != nil || (response.WorkMeasurement != nil && response.WorkMeasurement.Complete) {
			return fmt.Errorf("invalid remote result: failed work measurement claims output match")
		}
	default:
		return fmt.Errorf("invalid remote result: unknown work measurement state")
	}
	return nil
}
