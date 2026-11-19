package execute

import (
	"context"
	"fmt"
	"io"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/timing"
)

func (s *Service) runWorkMeasurementDiagnostic(ctx context.Context, req *model.RunRequest, hooks Hooks, tuning config.RuntimeTuningConfig) model.RunResponse {
	run := s.runWorkMeasurementWithStdin(ctx, req, nil, 0, hooks, tuning, false)
	response := finishWorkMeasurementTime(req, run.response, s.workMeasurement, tuning)
	return finishWorkMeasurementJudge(req, response, s.workMeasurement, tuning)
}

func (s *Service) runWorkMeasurementWithStdin(ctx context.Context, req *model.RunRequest, stdin io.Reader, stdinMaxBytes int64, hooks Hooks, tuning config.RuntimeTuningConfig, captureRawStdout bool) sandboxRunResult {
	// The watchdog and final resource checks must use the same raw envelope.
	rawReq := workMeasurementRawRequest(req)
	if !isShadowJudge(ctx) {
		rawReq.WorkMeasurementClock = effectiveWorkClock(req, s.workMeasurement, tuning)
	}
	rawService := *s
	rawService.cpuNormalizer = timing.CPUNormalizer{}
	rawService.shortCaseSampling = false
	run := rawService.runOneWithStdin(ctx, &rawReq, stdin, stdinMaxBytes, hooks, tuning, true, captureRawStdout)
	run.response = finishWorkMeasurementDiagnostic(ctx, req, run.response)
	if rawReq.WorkMeasurementClock != nil {
		run.response = finishWorkClock(req, rawReq.WorkMeasurementClock, run.response, run.workClockProgress)
	}
	return run
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
func discardIncompleteWorkMeasurement(interrupted bool, result *execResult) {
	if interrupted || result.Status != "OK" || result.ExitCode == nil || *result.ExitCode < 0 {
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
		report.ValidDiagnosticContract() && report.ValidREPTelemetry() && report.ValidLatencyBound() && report.ValidInstallationIdentity() &&
		((report.SignalsObserved == 0 && report.Quality == "uninterrupted") || (report.SignalsObserved > 0 && report.Quality == "signals_observed")) &&
		report.EngineProfile == durante.EngineProfile && report.Family == family &&
		(report.DispatchProfile == durante.DispatchDefault || (family == "cpp" && report.DispatchProfile == durante.DispatchGLIBC241 && report.HostLibc == "2.41")) &&
		report.Metric == durante.Metric && report.Scope == durante.Scope &&
		report.Instructions != nil && *report.Instructions > 0 && report.REPBytes != nil && report.REPEntries != nil &&
		(*report.REPEntries > 0 || *report.REPBytes == 0) &&
		report.UnsupportedREPEntries != nil && report.REPEnabled && report.Processes > 0 && report.Images >= report.Processes
}

// The nominal clock and the instrumented run's hard resource envelope have
// independent units. A deciding result must satisfy every supplied raw limit.
func withinRawMeasurementLimits(response model.RunResponse, raw durante.ResourceLimits) bool {
	return response.MemoryKB <= int64(raw.MemoryMB)*1024 &&
		response.CPUTimeNs <= uint64(raw.CPUTimeMs)*1_000_000 &&
		response.CPUTimeMs <= int64(raw.CPUTimeMs) && response.WallTimeMs <= int64(raw.WallTimeMs) &&
		(response.RawCPUTimeNs == nil || *response.RawCPUTimeNs == response.CPUTimeNs) &&
		(response.RawCPUTimeMs == nil || *response.RawCPUTimeMs == response.CPUTimeMs)
}

// Complete observations from the downstream runner must match the counter
// selected by the control plane. Interrupted roles may carry no totals.
func validateRemoteMeasurementCounters(req *model.RunRequest, response model.RunResponse, cfg durante.Config) error {
	if err := validateRemoteWorkMeasurement(req, response); err != nil {
		return err
	}
	matches := func(r *durante.Report) bool {
		return r == nil || !r.Complete || (r.LatencyBound != nil) == cfg.LatencyBound
	}
	if !matches(response.WorkMeasurement) {
		return fmt.Errorf("invalid remote result: work measurement latency counter mismatch")
	}
	for _, role := range response.WorkMeasurements {
		if !matches(role.Report) {
			return fmt.Errorf("invalid remote result: %s latency counter mismatch", role.Role)
		}
	}
	return nil
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
		// In judge mode a verdict (TLE from excess work, RE from an unconvertible
		// count) legitimately accompanies a complete report, so these raw-stop
		// consistency checks apply only to the pure diagnostic envelope.
		if !req.WorkMeasurementJudge {
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
	}
	if req.WorkMeasurementLimits == nil {
		if d != nil {
			return fmt.Errorf("invalid remote result: unsolicited work measurement execution")
		}
		return nil
	}
	// Judge mode deliberately returns a real verdict and score instead of the
	// diagnostic InitFail envelope; its status/score/source are recomputed and
	// checked by validateRemoteWorkMeasurementJudge. The pure diagnostic
	// envelope never returns a student verdict or normalized CPU.
	if !req.WorkMeasurementJudge {
		if response.Status != model.RunStatusInitFail || response.Score != nil || response.CPUTimeNormalization != nil {
			return fmt.Errorf("invalid remote result: work measurement envelope cannot return a student verdict or normalized CPU")
		}
	} else if response.CPUTimeNormalization != nil {
		return fmt.Errorf("invalid remote result: work measurement judge cannot return normalized CPU")
	}
	if d == nil { // Pre-execution validation/initialization failure.
		return nil
	}
	if d.Authority != durante.Authority || d.RawLimits != *req.WorkMeasurementLimits || d.NominalLimits != req.Limits {
		return fmt.Errorf("invalid remote result: work measurement envelope mismatch")
	}
	if (d.State == "complete" || d.State == "budget_exceeded") && !withinRawMeasurementLimits(response, d.RawLimits) {
		return fmt.Errorf("invalid remote result: work measurement exceeds an independent raw limit")
	}
	if !req.WorkMeasurementJudge && response.VerdictSource != "durante_"+d.State {
		return fmt.Errorf("invalid remote result: work measurement source mismatch")
	}
	switch d.State {
	case "complete":
		if d.OutputMatches == nil || !ordinaryMeasurementOutputSource(d.TerminationSource) || !validDiagnosticWorkReport(req.Lang, response.WorkMeasurement) || response.ExitCode == nil || *response.ExitCode != 0 {
			return fmt.Errorf("invalid remote result: incomplete work measurement marked complete")
		}
	case "budget_exceeded":
		switch d.TerminationSource {
		case "durante_work_clock_final":
			if d.OutputMatches == nil || !validDiagnosticWorkReport(req.Lang, response.WorkMeasurement) || response.ExitCode == nil || *response.ExitCode != 0 {
				return fmt.Errorf("invalid remote result: final budget crossing requires terminal totals")
			}
		case "durante_work_clock":
			if d.OutputMatches != nil || !response.WorkMeasurement.ValidUnavailableResult() || response.ExitCode != nil {
				return fmt.Errorf("invalid remote result: interrupted work clock claims terminal output")
			}
		default:
			return fmt.Errorf("invalid remote result: unknown work clock termination source")
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
