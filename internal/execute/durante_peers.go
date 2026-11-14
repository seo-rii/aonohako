package execute

import (
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// Collect only after the coordinator has observed process completion and closed
// its pipes. Collection must not delay peer shutdown or forfeit decisions. The
// interruption flag is captured when the executor returns, before cleanup
// cancels the role's context unconditionally.
func collectPeerWorkMeasurement(meter *durante.Session, result execResult, interrupted bool, role, programID string, participantIndex *int) model.WorkMeasurementProcess {
	var report *durante.Report
	if meter != nil {
		report = meter.Collect(result.targetPID)
		if interrupted || result.Status != "OK" || result.ExitCode == nil || *result.ExitCode < 0 {
			report = report.WithoutTotals("measurement execution did not complete normally")
		}
	}
	return model.WorkMeasurementProcess{
		Role:              role,
		ProgramID:         programID,
		ParticipantIndex:  participantIndex,
		ExecutionStatus:   result.Status,
		ExitCode:          result.ExitCode,
		TerminationSource: result.VerdictSource,
		Report:            report,
	}
}
