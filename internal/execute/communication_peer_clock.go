package execute

import (
	"errors"
	"fmt"
	"io"
	"time"

	"aonohako/internal/model"
)

// In legacy communication an early participant failure traditionally wins over
// the subsequent manager shutdown. In clock mode only a checked participant
// result can supply that failure, and any independent manager failure is unscored.
func finalizeLegacyCommunicationClock(req *model.RunRequest, response *model.RunResponse, processes []communicationProcessResult, protocol communicationManagerProtocol, firstFailure *communicationProcessResult, canceledAt time.Time, requestErr error) {
	fail := func(reason, source string) {
		response.Status, response.Score = model.RunStatusInitFail, nil
		response.Reason, response.VerdictSource = reason, source
	}
	if requestErr != nil || response.StartedParticipants != req.Communication.ParticipantCount {
		fail("communication did not finish starting all roles or was cancelled", "communication:peer_clock")
		return
	}
	var manager *communicationProcessResult
	for i := range processes {
		p := &processes[i]
		if communicationParticipantInfrastructureFailure(*p) {
			fail("communication role clock or raw resource check failed", "communication:peer_clock")
			return
		}
		if p.manager {
			manager = p
			continue
		}
		if communicationIndependentParticipantFault(*p, canceledAt) &&
			(firstFailure == nil || p.completedAt.Before(firstFailure.completedAt)) {
			firstFailure = p
		}
	}
	if manager == nil {
		fail("communication manager did not start", "manager:init")
		return
	}
	managerOK := manager.result.Status == "OK" && manager.result.ExitCode != nil && *manager.result.ExitCode == 0 && protocol.err == nil
	interruptedProtocol := (errors.Is(protocol.err, io.EOF) || errors.Is(protocol.err, io.ErrUnexpectedEOF)) &&
		!protocol.completedAt.IsZero() && !protocol.completedAt.Before(canceledAt)
	siblingStop := firstFailure != nil && !canceledAt.IsZero() && !manager.completedAt.Before(canceledAt) &&
		manager.result.VerdictSource == "durante_peer_cancelled" && (protocol.err == nil || interruptedProtocol)
	if !managerOK && !siblingStop {
		fail("communication manager or its result protocol failed", "manager:runtime")
		return
	}
	if firstFailure != nil {
		status, reason, source := communicationFinalProcessStatus(*firstFailure)
		if status == model.RunStatusInitFail || source == "durante_peer_cancelled" || status == "OK" && (firstFailure.result.ExitCode == nil || *firstFailure.result.ExitCode == 0) {
			fail("communication participant failed without an independent observation", "communication:peer_clock")
			return
		}
		if status == "OK" {
			status, source = model.RunStatusRE, "exit_code"
		}
		response.Status, response.VerdictSource = status, fmt.Sprintf("participant:%d:%s", firstFailure.participant, source)
		response.Reason = firstNonEmptyString(reason, firstFailure.result.Reason, "communication participant failed")
		zero := 0.0
		response.Score = &zero
	}
}

// Validate the coordinator's use of role results after the control plane has
// reconstructed every v2 clock receipt. An independent watchdog or unavailable
// clock may never account for a scored match or a participant time forfeit.
func validateCommunicationPeerClockVerdict(req *model.RunRequest, response model.RunResponse) error {
	if req.Communication == nil || req.WorkMeasurementPeerTime == nil || req.WorkMeasurementPeerTime.Version != 2 {
		return nil
	}
	invalid := errors.New("invalid remote result: communication outcome disagrees with verified role clocks")
	if response.Status == model.RunStatusInitFail && response.Score == nil {
		if response.Match != nil && (response.Match.Outcome != "judge_error" || response.Match.Wins != nil) {
			return invalid
		}
		return nil
	}
	var managerOK, managerCancelled bool
	failedSeat := -1
	failedSource, failedStatus := "", ""
	legacySelectedFault := false
	if len(response.WorkMeasurements) != req.Communication.ParticipantCount+1 || response.StartedParticipants != req.Communication.ParticipantCount {
		return invalid
	}
	for _, role := range response.WorkMeasurements {
		ok := role.ExecutionStatus == "OK" && role.ExitCode != nil && *role.ExitCode == 0
		cancelled := role.ExecutionStatus == model.RunStatusInitFail && role.TerminationSource == "durante_peer_cancelled"
		if role.Role == "manager" {
			managerOK, managerCancelled = ok, cancelled
			continue
		}
		if role.ParticipantIndex == nil {
			return invalid
		}
		if ok || cancelled {
			continue
		}
		if failedSeat >= 0 && req.Communication.Version == 2 {
			return invalid
		}
		failedSeat = *role.ParticipantIndex
		switch {
		case role.ExecutionStatus == model.RunStatusTLE && (role.TerminationSource == "durante_work_clock" || role.TerminationSource == "durante_work_clock_final") &&
			role.TimeBudget != nil && role.TimeBudget.Exceeded != nil && *role.TimeBudget.Exceeded:
			failedStatus, failedSource = model.RunStatusTLE, role.TerminationSource
		case role.ExecutionStatus == "OK" && role.ExitCode != nil && *role.ExitCode != 0:
			failedStatus, failedSource = model.RunStatusRE, "exit_code"
		default:
			return invalid
		}
		if response.Status == failedStatus && response.VerdictSource == fmt.Sprintf("participant:%d:%s", failedSeat, failedSource) {
			legacySelectedFault = true
		}
	}
	if !managerOK && !(failedSeat >= 0 && managerCancelled) {
		return invalid
	}
	if req.Communication.Version == 1 {
		if response.Match != nil || response.Score == nil {
			return invalid
		}
		if failedSeat >= 0 {
			if !legacySelectedFault || *response.Score != 0 {
				return invalid
			}
		} else if response.VerdictSource != "manager" || (response.Status != model.RunStatusAccepted && response.Status != model.RunStatusWA) {
			return invalid
		}
		return nil
	}
	m := response.Match
	if m == nil || response.Score != nil || response.Status != model.RunStatusAccepted || response.VerdictSource != "communication:match" ||
		m.Version != 1 || m.Games != communicationMatchGames || m.MatchSeed != req.Communication.MatchSeed || m.InputSHA256 != req.Communication.InputSHA256 || m.RuntimeFingerprint != req.Communication.RuntimeFingerprint ||
		m.Wins == nil || len(m.Participants) != 2 || m.Retriable {
		return invalid
	}
	programs := communicationParticipantPrograms(req)
	for i, seat := range m.Participants {
		if seat.Seat != i || seat.ProgramID != programs[i].ID || seat.SourceSHA256 != programs[i].SourceSHA256 || m.Wins[i] < 0 || m.Wins[i] > communicationMatchGames {
			return invalid
		}
	}
	if m.Wins[0]+m.Wins[1] != communicationMatchGames {
		return invalid
	}
	if failedSeat < 0 {
		if m.Outcome != "completed" {
			return invalid
		}
		for _, seat := range m.Participants {
			if seat.Status != "OK" || (seat.VerdictSource != "" && seat.VerdictSource != "communication:manager_shutdown") {
				return invalid
			}
		}
	} else {
		if failedSeat >= 2 || m.Outcome != "forfeit" || m.Wins[1-failedSeat] != communicationMatchGames || m.Wins[failedSeat] != 0 {
			return invalid
		}
		winner := m.Participants[1-failedSeat]
		if (winner.Status != "OK" && !(winner.Status == "not_finished" && winner.VerdictSource == "communication:forfeit_shutdown")) ||
			m.Participants[failedSeat].Status != failedStatus || m.Participants[failedSeat].VerdictSource != failedSource {
			return invalid
		}
	}
	return nil
}
