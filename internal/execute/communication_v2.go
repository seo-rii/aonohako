package execute

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"aonohako/internal/model"
)

// Competitive matches execute one official twenty-game fixture. Tournament
// scores are computed by the trusted caller; the runner never emits an AC score.
const communicationMatchGames = 20

func communicationFileMatchesSHA256(path, expected string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false, err
	}
	return hex.EncodeToString(hash.Sum(nil)) == expected, nil
}

type communicationMatchManagerResult struct {
	Version int   `json:"version"`
	Wins    []int `json:"wins"`
	Games   int   `json:"games"`
}

type communicationIncompleteMatchResultError struct {
	cause error
}

func (e *communicationIncompleteMatchResultError) Error() string { return e.cause.Error() }
func (e *communicationIncompleteMatchResultError) Unwrap() error { return e.cause }

func communicationMatchResultIncomplete(err error) bool {
	var incomplete *communicationIncompleteMatchResultError
	return errors.As(err, &incomplete)
}

func communicationParticipantPrograms(req *model.RunRequest) []model.RunProgram {
	if req.Communication.Version == 1 {
		participant, _ := communicationPrograms(req)
		return []model.RunProgram{participant}
	}
	programs := make([]model.RunProgram, len(req.Communication.ParticipantProgramIDs))
	for seat, id := range req.Communication.ParticipantProgramIDs {
		for _, program := range req.Programs {
			if program.ID == id {
				programs[seat] = program
				break
			}
		}
	}
	return programs
}

func readCommunicationResult(reader io.Reader, version int) (communicationManagerResult, error) {
	if version == 1 {
		return readCommunicationManagerResult(reader)
	}
	data, err := io.ReadAll(io.LimitReader(reader, communicationResultMaxBytes+1))
	if err != nil {
		return communicationManagerResult{}, err
	}
	if len(data) > communicationResultMaxBytes {
		return communicationManagerResult{}, fmt.Errorf("manager result exceeds %d bytes", communicationResultMaxBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var result communicationMatchManagerResult
	if err := decoder.Decode(&result); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			err = &communicationIncompleteMatchResultError{cause: err}
		}
		return communicationManagerResult{}, fmt.Errorf("invalid match manager result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return communicationManagerResult{}, fmt.Errorf("manager result must contain exactly one JSON object")
	}
	if result.Version != 1 || result.Games != communicationMatchGames || len(result.Wins) != 2 ||
		result.Wins[0] < 0 || result.Wins[1] < 0 || result.Wins[0] > result.Games ||
		result.Wins[1] > result.Games || result.Wins[0]+result.Wins[1] != result.Games {
		return communicationManagerResult{}, fmt.Errorf("match-result-v1 requires version 1, games 20, and two nonnegative win counts totaling 20")
	}
	return communicationManagerResult{match: &result}, nil
}

func communicationMatchFailure(req *model.RunRequest, message string) *model.MatchResult {
	result := &model.MatchResult{
		Version: 1, Outcome: "judge_error", Games: communicationMatchGames,
		Retriable: true, Message: message, MatchSeed: req.Communication.MatchSeed,
		InputSHA256:        req.Communication.InputSHA256,
		RuntimeFingerprint: req.Communication.RuntimeFingerprint,
		Participants:       []model.MatchParticipant{},
	}
	for seat, program := range communicationParticipantPrograms(req) {
		participant := model.MatchParticipant{
			Seat: seat, ProgramID: program.ID, SourceSHA256: program.SourceSHA256,
			EntryPoint: program.EntryPoint,
			Status:     "not_finished", Artifacts: []model.MatchArtifact{},
		}
		for _, binary := range program.Binaries {
			participant.Artifacts = append(participant.Artifacts, model.MatchArtifact{Name: binary.Name, SHA256: binary.SHA256})
		}
		result.Participants = append(result.Participants, participant)
	}
	return result
}

func communicationParticipantInfrastructureFailure(process communicationProcessResult) bool {
	return process.result.Status == model.RunStatusInitFail ||
		process.result.VerdictSource == "sandbox_helper_oom" ||
		process.result.VerdictSource == "sandbox_init"
}

func communicationFinalProcessStatus(process communicationProcessResult) (string, string, string) {
	status, reason, source := classifyRunStatusWithoutOutput(process.request, process.result)
	if communicationParticipantInfrastructureFailure(process) {
		return status, reason, source
	}
	// Final target accounting must not disappear when stream shutdown or a
	// coordinator kill changed the raw status before the supervisor returned.
	if limit := process.request.Limits.TimeMs; limit > 0 && process.result.CPUTimeMs > int64(limit) {
		return model.RunStatusTLE, "cpu time limit exceeded", "cpu_time_final"
	}
	if limit := process.request.Limits.MemoryMB; limit > 0 && process.result.MemoryKB > int64(limit)*1024 {
		return model.RunStatusMLE, "memory limit exceeded", "memory_reported"
	}
	return status, reason, source
}

func communicationIndependentParticipantFault(process communicationProcessResult, canceledAt time.Time) bool {
	status, _, source := communicationFinalProcessStatus(process)
	if (status == "OK" && process.result.ExitCode != nil && *process.result.ExitCode == 0) || communicationParticipantInfrastructureFailure(process) {
		return false
	}
	if process.result.ExitCode != nil && *process.result.ExitCode != 0 {
		return true
	}
	if !process.wallDeadlineAt.IsZero() && !process.completedAt.Before(process.wallDeadlineAt) &&
		(canceledAt.IsZero() || !process.wallDeadlineAt.After(canceledAt)) {
		return true
	}
	// These two sources can be generated by coordinator shutdown. Resource
	// accounting, nonzero target exits and target signals remain real failures
	// even if their supervisor reports them after the manager's result.
	return source != "wall_time" && source != "stream_io"
}

func buildCommunicationMatchResponse(req *model.RunRequest, processes []communicationProcessResult,
	managerResult *communicationMatchManagerResult, managerResultErr error, startedParticipants int,
	wallTimeMs int64, firstFailure *communicationProcessResult, participantCancellationAt time.Time,
	requestErr error) model.RunResponse {
	response := model.RunResponse{
		Status: model.RunStatusRE, TimeMs: wallTimeMs, WallTimeMs: wallTimeMs,
		VerdictSource: "communication:match", StartedParticipants: startedParticipants,
		Match: communicationMatchFailure(req, "competitive match did not complete"),
	}
	var manager *communicationProcessResult
	for i := range processes {
		process := &processes[i]
		response.CPUTimeMs += process.result.CPUTimeMs
		response.ProcessCPUTimeMs += process.result.ProcessCPUTimeMs
		response.MemoryKB += process.result.MemoryKB
		if process.manager {
			manager = process
		} else if process.participant >= 0 && process.participant < len(response.Match.Participants) {
			seat := &response.Match.Participants[process.participant]
			seat.CPUTimeMs, seat.MemoryKB = process.result.CPUTimeMs, process.result.MemoryKB
			seat.EntryPoint = process.entryPoint
			seat.Status, seat.Reason, seat.VerdictSource = communicationFinalProcessStatus(*process)
		}
	}
	fail := func(reason, source string) model.RunResponse {
		response.Reason, response.VerdictSource = reason, source
		response.Match.Message = reason
		return response
	}
	if requestErr != nil {
		return fail("competitive match request was canceled", "communication:request_canceled")
	}
	if startedParticipants != 2 || manager == nil {
		return fail("competitive match did not start both participants and manager", "communication:startup")
	}
	for _, process := range processes {
		if !process.manager && communicationParticipantInfrastructureFailure(process) {
			// Cancellation before a target starts is infrastructure failure; it
			// cannot establish the other submitted program as a valid winner.
			return fail("competitive participant sandbox failed", "communication:participant:init")
		}
	}
	managerStatus, _, managerSource := communicationFinalProcessStatus(*manager)
	managerIndependentDeadline := !manager.wallDeadlineAt.IsZero() &&
		!manager.completedAt.Before(manager.wallDeadlineAt) &&
		(participantCancellationAt.IsZero() || !manager.wallDeadlineAt.After(participantCancellationAt))
	managerValid := !managerIndependentDeadline && managerStatus == "OK" && manager.result.ExitCode != nil && *manager.result.ExitCode == 0 && managerResultErr == nil && managerResult != nil
	managerInterruptedProtocol := communicationMatchResultIncomplete(managerResultErr) &&
		!manager.resultProtocolAt.IsZero() && !manager.resultProtocolAt.Before(participantCancellationAt)
	managerCanceledForForfeit := firstFailure != nil && !participantCancellationAt.IsZero() &&
		!manager.completedAt.Before(participantCancellationAt) && (manager.result.ExitCode == nil || *manager.result.ExitCode == 0) &&
		managerStatus == model.RunStatusTLE && managerSource == "wall_time" && !managerIndependentDeadline &&
		((managerResultErr == nil && managerResult != nil) || managerInterruptedProtocol)
	if !managerValid && !managerCanceledForForfeit {
		return fail("competitive manager process or result protocol failed independently", "manager:runtime")
	}
	for i := range processes {
		process := &processes[i]
		if process.manager || !communicationIndependentParticipantFault(*process, participantCancellationAt) {
			continue
		}
		if firstFailure == nil || !communicationIndependentParticipantFault(*firstFailure, participantCancellationAt) ||
			process.completedAt.Before(firstFailure.completedAt) {
			firstFailure = process
		}
	}
	managerFinishedBeforeFailure := managerValid && (firstFailure == nil ||
		(!communicationIndependentParticipantFault(*firstFailure, participantCancellationAt) &&
			(firstFailure.result.VerdictSource == "stream_io" || !manager.completedAt.After(firstFailure.completedAt))))
	if managerFinishedBeforeFailure {
		response.Status = model.RunStatusAccepted
		response.Match.Outcome, response.Match.Retriable = "completed", false
		response.Match.Wins = &[2]int{managerResult.Wins[0], managerResult.Wins[1]}
		response.Match.Message = ""
	} else if firstFailure != nil {
		// A manager failure which already happened cannot be relabeled a
		// contestant forfeit when its pipe shutdown subsequently kills a seat.
		if !managerValid && !manager.completedAt.IsZero() && manager.completedAt.Before(firstFailure.completedAt) {
			return fail("competitive manager failed before participant failure", "manager:runtime")
		}
		failedSeats := map[int]bool{firstFailure.participant: true}
		for _, process := range processes {
			if process.manager || process.participant == firstFailure.participant || !communicationProcessFailed(process) {
				continue
			}
			if process.result.VerdictSource == "stream_io" && !communicationIndependentParticipantFault(process, participantCancellationAt) {
				managerResultPrecededStream := managerValid &&
					((!manager.resultProtocolAt.IsZero() && !process.completedAt.Before(manager.resultProtocolAt)) ||
						!process.completedAt.Before(manager.completedAt))
				if managerResultPrecededStream || (!participantCancellationAt.IsZero() && !process.completedAt.Before(participantCancellationAt)) {
					continue
				}
			}
			if communicationIndependentParticipantFault(process, participantCancellationAt) || (!participantCancellationAt.IsZero() && !process.completedAt.After(participantCancellationAt)) {
				failedSeats[process.participant] = true
			}
		}
		if len(failedSeats) != 1 {
			return fail("both competitive participants failed independently; match is unscored", "communication:double_forfeit")
		}
		response.Status = model.RunStatusAccepted
		response.Match.Outcome, response.Match.Retriable = "forfeit", false
		wins := [2]int{}
		wins[1-firstFailure.participant] = communicationMatchGames
		response.Match.Wins = &wins
		response.Match.Message = fmt.Sprintf("participant %d forfeited all 20 games", firstFailure.participant)
	} else {
		return fail("competitive manager did not produce a valid match-result-v1 result", "manager:result_protocol")
	}
	for _, process := range processes {
		if process.manager || process.participant < 0 || process.participant >= len(response.Match.Participants) {
			continue
		}
		seat := &response.Match.Participants[process.participant]
		seat.CPUTimeMs, seat.MemoryKB = process.result.CPUTimeMs, process.result.MemoryKB
		seat.Status, seat.Reason, seat.VerdictSource = communicationFinalProcessStatus(process)
		if response.Match.Outcome == "forfeit" && process.participant != firstFailure.participant && communicationProcessFailed(process) {
			seat.Status, seat.Reason, seat.VerdictSource = "not_finished", "stopped after opponent forfeit", "communication:forfeit_shutdown"
		} else if response.Match.Outcome == "completed" && communicationProcessFailed(process) && !communicationIndependentParticipantFault(process, participantCancellationAt) && process.completedAt.After(manager.completedAt) {
			seat.Status, seat.Reason, seat.VerdictSource = "OK", "", "communication:manager_shutdown"
		}
	}
	return response
}
