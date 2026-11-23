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
	// Native CPU policy may already have collected the same report before
	// interactive verdict selection. Reuse it without parsing or hashing twice.
	report := result.WorkMeasurement
	if report == nil && meter != nil && result.workClockReceipt == nil {
		report = meter.Collect(result.targetPID)
	}
	if report != nil && result.workClockReceipt == nil {
		if interrupted || result.Status != "OK" || result.ExitCode == nil || *result.ExitCode < 0 {
			report = report.WithoutTotals("measurement execution did not complete normally")
		}
	}
	var budget *model.PeerTimeBudgetResult
	if result.workClockReceipt != nil {
		budget = &model.PeerTimeBudgetResult{Clock: result.workClockReceipt, RawUsage: result.peerClockUsage, RawSource: result.peerClockRawSource}
	}
	return model.WorkMeasurementProcess{
		TimeBudget:        budget,
		Role:              role,
		ProgramID:         programID,
		ParticipantIndex:  participantIndex,
		ExecutionStatus:   result.Status,
		ExitCode:          result.ExitCode,
		TerminationSource: result.VerdictSource,
		Report:            report,
	}
}
