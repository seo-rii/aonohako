package execute

import (
	"strings"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func publicStageClockSource(source string, interactive bool) bool {
	if interactive {
		role, suffix, ok := strings.Cut(source, ":")
		if !ok || role != "contestant" && role != "interactor" && role != "interactive" {
			return false
		}
		source = suffix
	}
	switch source {
	case "durante_work_clock", "durante_work_clock_final", "durante_peer_clock_error", "durante_peer_cancelled":
		return true
	}
	return false
}

func stageFinalCheckerFailure(req *model.RunRequest, r *model.RunResponse) bool {
	if !stagedPeerClocksRequested(req) || req.Pipeline == nil || req.Pipeline.FinalJudge.SPJ == nil || r.Status != model.RunStatusInitFail || r.Score != nil || len(req.Pipeline.Steps) != len(r.Steps) || len(r.Steps) == 0 || r.Steps[len(r.Steps)-1].Status != model.RunStatusAccepted {
		return false
	}
	switch r.VerdictSource {
	case "final:checker:spj", "final:checker:durante_peer_clock_error", "final:checker:durante_work_clock", "final:checker:durante_work_clock_final":
		return true
	}
	return false
}

// Incomplete role observations can contain free-form diagnostics. Keep the
// clock receipt, supported installation identity and fixed protocol codes.
func redactStagedClockRows(rows []model.WorkMeasurementProcess) {
	for i := range rows {
		row := &rows[i]
		if report := row.Report; report != nil {
			r := *report
			if r.Complete {
				r.Reason = ""
			} else {
				r = *r.WithoutTotals("stage work observation unavailable")
			}
			r.MeasurementContract = durante.DiagnosticContract()
			if durante.ValidateMeasurementFamily(r.Family) != nil {
				r.Family = ""
			}
			if r.EngineProfile != durante.EngineProfile {
				r.EngineProfile = ""
			}
			if r.DispatchProfile != durante.DispatchGLIBC241 {
				r.HostLibc = ""
				if r.DispatchProfile != durante.DispatchDefault {
					r.DispatchProfile = ""
				}
			} else if r.HostLibc != "2.41" {
				r.HostLibc = ""
			}
			if !r.ValidInstallationIdentity() {
				r.Installation = nil
			}
			r.DisallowedCPUFeatures = nil // Stage launchers reject CPU feature restrictions.
			row.Report = &r
		}
		if in := row.TimeBudget; in != nil {
			b := *in
			switch source := b.RawSource; source {
			case "", "stage_not_started", "peer_context", "request_context", "clock_unavailable", "raw_execution", "durante_work_clock", "durante_work_clock_cleanup":
			default:
				if !publicPipelineStepSource(model.StepResult{VerdictSource: source}, false) {
					b.RawSource = "raw_execution"
				}
			}
			row.TimeBudget = &b
		}
	}
}
