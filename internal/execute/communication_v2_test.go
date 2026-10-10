package execute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

func competitiveRequest() *model.RunRequest {
	return &model.RunRequest{
		Programs: []model.RunProgram{
			{ID: "manager", Lang: "binary"},
			{ID: "seat1", SourceSHA256: strings.Repeat("b", 64), Binaries: []model.Binary{{Name: "other", SHA256: strings.Repeat("2", 64)}}},
			{ID: "seat0", SourceSHA256: strings.Repeat("a", 64), Binaries: []model.Binary{{Name: "main", SHA256: strings.Repeat("1", 64)}}},
		},
		Communication: &model.CommunicationSpec{
			Version: 2, ParticipantProgramIDs: []string{"seat0", "seat1"}, ManagerProgramID: "manager",
			ParticipantCount: 2, ResultProtocol: "match-result-v1", MatchSeed: strings.Repeat("3", 64),
			InputSHA256:        "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			RuntimeFingerprint: strings.Repeat("a", 64),
		},
		Limits: model.Limits{TimeMs: 2000, MemoryMB: 1024},
	}
}

func TestCompetitiveRuntimeMismatchAndDisabledRejectBeforeLaunching(t *testing.T) {
	for _, image := range []string{"", "sha256:" + strings.Repeat("1", 64)} {
		service := NewWithConfig(config.Config{
			CompetitiveRuntimeFingerprint: image, CommunicationEnabled: true,
			Execution: config.ExecutionConfig{Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetCloudRun,
				ExecutionTransport: platform.ExecutionTransportEmbedded, SandboxBackend: platform.SandboxBackendHelper,
			}},
		})
		req := competitiveRequest()
		hash := sha256.Sum256([]byte("x"))
		for i := range req.Programs {
			req.Programs[i].Lang = "binary"
			req.Programs[i].SourceSHA256 = strings.Repeat("a", 64)
			req.Programs[i].Binaries = []model.Binary{{Name: "main", DataB64: "eA==", SHA256: hex.EncodeToString(hash[:]), Mode: "exec"}}
		}
		response := service.Run(context.Background(), req, Hooks{})
		if response.Match == nil || response.Match.Outcome != "judge_error" || response.Match.Retriable || response.Match.ErrorCode != "runtime_fingerprint" || response.StartedParticipants != 0 || response.Match.Wins != nil {
			t.Fatalf("unpinned runtime launched targets: %+v", response)
		}
		if response.Match.RuntimeFingerprint != service.competitiveRuntimeIdentity.RuntimeFingerprint {
			t.Fatalf("runner echoed caller's claimed identity: %+v", response.Match)
		}
	}
}

func competitiveProcesses(at time.Time) []communicationProcessResult {
	zero := 0
	req := &model.RunRequest{Limits: model.Limits{TimeMs: 2000, MemoryMB: 1024}}
	return []communicationProcessResult{
		{participant: 0, request: req, result: execResult{Status: "OK", ExitCode: &zero, CPUTimeMs: 17, MemoryKB: 27}, completedAt: at},
		{participant: 1, request: req, result: execResult{Status: "OK", ExitCode: &zero, CPUTimeMs: 19, MemoryKB: 29}, completedAt: at},
		{manager: true, participant: -1, request: req, result: execResult{Status: "OK", ExitCode: &zero}, completedAt: at},
	}
}

func TestReadCommunicationMatchManagerResult(t *testing.T) {
	result, err := readCommunicationResult(strings.NewReader(`{"version":1,"wins":[7,13],"games":20}`), 2)
	if err != nil || result.match == nil || result.match.Wins[1] != 13 {
		t.Fatalf("valid result = %+v, %v", result, err)
	}
	for _, input := range []string{
		`{"version":1,"wins":[10,10,0],"games":20}`,
		`{"version":1,"wins":[7,7],"games":20}`,
		`{"version":1,"wins":[-1,21],"games":20}`,
		`{"version":1,"wins":[0,20],"games":19}`,
		`{"version":2,"wins":[0,20],"games":20}`,
		`{"wins":[0,20],"games":20}`,
		`{"version":1,"wins":[0,20],"games":20,"score":1}`,
		`{"version":1,"wins":[0,20],"games":20}{}`,
	} {
		if _, err := readCommunicationResult(strings.NewReader(input), 2); err == nil {
			t.Fatalf("invalid result accepted: %s", input)
		}
	}
}

func TestCompetitiveResponseBindsOrderedProgramsWithoutSingleScore(t *testing.T) {
	at := time.Now()
	result := &communicationMatchManagerResult{Version: 1, Wins: []int{7, 13}, Games: 20}
	response := buildCommunicationMatchResponse(competitiveRequest(), competitiveProcesses(at), result, nil, 2, 10, nil, time.Time{}, nil)
	if response.Status != model.RunStatusAccepted || response.Score != nil || response.Match.Outcome != "completed" || response.Match.Retriable || *response.Match.Wins != [2]int{7, 13} {
		t.Fatalf("unexpected completed response: %+v", response)
	}
	if response.Match.Participants[0].ProgramID != "seat0" || response.Match.Participants[1].ProgramID != "seat1" || response.Match.Participants[0].Artifacts[0].SHA256 != strings.Repeat("1", 64) || response.Match.Participants[0].SourceSHA256 != strings.Repeat("a", 64) || response.Match.MatchSeed != strings.Repeat("3", 64) {
		t.Fatalf("bindings wrong: %+v", response.Match)
	}
}

func TestCompetitiveForfeitPreservesPeerFromCancellation(t *testing.T) {
	for _, status := range []string{model.RunStatusRE, model.RunStatusTLE, model.RunStatusMLE} {
		t.Run(status, func(t *testing.T) {
			at := time.Now()
			processes := competitiveProcesses(at)
			processes[0].result = execResult{Status: status, Reason: "participant failure", VerdictSource: "runtime"}
			processes[1].result = execResult{Status: model.RunStatusRE, VerdictSource: "wall_time"}
			processes[1].completedAt = at.Add(2 * time.Millisecond)
			processes[2].result = execResult{Status: model.RunStatusTLE, VerdictSource: "wall_time"}
			processes[2].completedAt = at.Add(3 * time.Millisecond)
			processes[2].resultProtocolAt = at.Add(3 * time.Millisecond)
			_, protocolErr := readCommunicationResult(strings.NewReader(""), 2)
			response := buildCommunicationMatchResponse(competitiveRequest(), processes, nil, protocolErr, 2, 10, &processes[0], at.Add(time.Millisecond), nil)
			if response.Match.Outcome != "forfeit" || response.Match.Retriable || *response.Match.Wins != [2]int{0, 20} || response.Match.Participants[0].Status != status || response.Match.Participants[1].Status != "not_finished" {
				t.Fatalf("unexpected forfeit: %+v", response.Match)
			}
		})
	}
}

func TestCompetitiveUnscoredFailures(t *testing.T) {
	for _, name := range []string{"double fault", "manager first", "participant infrastructure", "invalid manager protocol", "canceled request", "incomplete startup"} {
		t.Run(name, func(t *testing.T) {
			at := time.Now()
			processes := competitiveProcesses(at)
			var firstFailure *communicationProcessResult
			var requestErr error
			var result *communicationMatchManagerResult
			var protocolErr error
			started := 2
			switch name {
			case "double fault":
				processes[0].result = execResult{Status: model.RunStatusRE}
				processes[1].result = execResult{Status: model.RunStatusRE}
				processes[2].completedAt = at.Add(3 * time.Millisecond)
				firstFailure = &processes[0]
				result = &communicationMatchManagerResult{Version: 1, Wins: []int{10, 10}, Games: 20}
			case "manager first":
				processes[2].result = execResult{Status: model.RunStatusRE}
				processes[2].completedAt = at.Add(-time.Millisecond)
				processes[0].result = execResult{Status: model.RunStatusRE}
				firstFailure = &processes[0]
			case "participant infrastructure":
				processes[0].result = execResult{Status: model.RunStatusInitFail, VerdictSource: "sandbox_init"}
				firstFailure = &processes[0]
			case "invalid manager protocol":
				protocolErr = errors.New("protocol")
			case "canceled request":
				requestErr = errors.New("canceled")
			case "incomplete startup":
				started = 1
			}
			response := buildCommunicationMatchResponse(competitiveRequest(), processes, result, protocolErr, started, 10, firstFailure, at.Add(time.Millisecond), requestErr)
			if response.Match.Outcome != "judge_error" || !response.Match.Retriable || response.Match.Wins != nil || response.Score != nil {
				t.Fatalf("infra error must remain unscored: %+v", response.Match)
			}
			if name == "double fault" && response.VerdictSource != "communication:double_forfeit" {
				t.Fatalf("independent double fault lost attribution: %+v", response)
			}
		})
	}
}

func TestCompetitiveManagerResultPrecedesTeardownSignals(t *testing.T) {
	at := time.Now()
	processes := competitiveProcesses(at)
	processes[0].result = execResult{Status: model.RunStatusRE, VerdictSource: "stream_io"}
	processes[0].completedAt = at.Add(time.Millisecond)
	result := &communicationMatchManagerResult{Version: 1, Wins: []int{7, 13}, Games: 20}
	response := buildCommunicationMatchResponse(competitiveRequest(), processes, result, nil, 2, 10, &processes[0], at.Add(2*time.Millisecond), nil)
	if response.Match.Outcome != "completed" || *response.Match.Wins != [2]int{7, 13} || response.Match.Participants[0].Status != "OK" {
		t.Fatalf("final manager result overwritten by shutdown: %+v", response.Match)
	}
}

func TestCompetitiveActualManagerExitDoesNotBecomeForfeitFromPipeRace(t *testing.T) {
	at := time.Now()
	processes := competitiveProcesses(at)
	processes[0].result = execResult{Status: model.RunStatusRE, VerdictSource: "exit_code"}
	processes[2].completedAt = at.Add(time.Millisecond)
	exit := 29
	processes[2].result = execResult{Status: "OK", ExitCode: &exit}
	processes[2].resultProtocolAt = at.Add(time.Millisecond)
	_, protocolErr := readCommunicationResult(strings.NewReader(""), 2)
	response := buildCommunicationMatchResponse(competitiveRequest(), processes, nil, protocolErr, 2, 10, &processes[0], at.Add(time.Millisecond), nil)
	if response.Match.Outcome != "judge_error" || !response.Match.Retriable || response.Match.Wins != nil {
		t.Fatalf("manager's actual failure was hidden by a pipe EOF race: %+v", response.Match)
	}
}

func TestCompetitiveLateResourceFaultOverridesManagerResult(t *testing.T) {
	for _, fault := range []struct {
		status, source string
		cpu, memory    int64
		expectedStatus string
	}{
		{model.RunStatusTLE, "cpu_time_final", 0, 0, model.RunStatusTLE}, {model.RunStatusMLE, "memory_cgroup_final", 0, 0, model.RunStatusMLE},
		{model.RunStatusRE, "exit_code", 0, 0, model.RunStatusRE}, {model.RunStatusRE, "signal", 0, 0, model.RunStatusRE},
		{model.RunStatusTLE, "wall_time", 2001, 0, model.RunStatusTLE}, {model.RunStatusRE, "stream_io", 2001, 0, model.RunStatusTLE},
		{model.RunStatusTLE, "wall_time", 0, 1024*1024 + 1, model.RunStatusMLE}, {model.RunStatusRE, "stream_io", 0, 1024*1024 + 1, model.RunStatusMLE},
	} {
		t.Run(fault.source, func(t *testing.T) {
			at := time.Now()
			processes := competitiveProcesses(at)
			processes[0].result = execResult{Status: fault.status, VerdictSource: fault.source, CPUTimeMs: fault.cpu, MemoryKB: fault.memory}
			processes[0].completedAt = at.Add(time.Millisecond)
			result := &communicationMatchManagerResult{Version: 1, Wins: []int{7, 13}, Games: 20}
			response := buildCommunicationMatchResponse(competitiveRequest(), processes, result, nil, 2, 10, nil, time.Time{}, nil)
			if response.Match.Outcome != "forfeit" || *response.Match.Wins != [2]int{0, 20} || response.Match.Participants[0].Status != fault.expectedStatus {
				t.Fatalf("real late resource or target failure was treated as shutdown: %+v", response.Match)
			}
		})
	}
}

func TestCompetitiveManagerCancellationCannotHideResourceFaultOrUnobservedCause(t *testing.T) {
	for _, mode := range []string{"cpu", "memory", "unobserved cancellation", "cancellation after completion"} {
		t.Run(mode, func(t *testing.T) {
			at := time.Now()
			processes := competitiveProcesses(at)
			processes[0].result = execResult{Status: model.RunStatusRE, VerdictSource: "exit_code"}
			processes[2].result = execResult{Status: model.RunStatusTLE, VerdictSource: "wall_time"}
			processes[2].completedAt = at.Add(3 * time.Millisecond)
			processes[2].resultProtocolAt = at.Add(3 * time.Millisecond)
			canceledAt := at.Add(time.Millisecond)
			switch mode {
			case "cpu":
				processes[2].result.CPUTimeMs = 2001
			case "memory":
				processes[2].result.MemoryKB = 1024*1024 + 1
			case "unobserved cancellation":
				canceledAt = time.Time{}
			case "cancellation after completion":
				canceledAt = at.Add(4 * time.Millisecond)
			}
			_, protocolErr := readCommunicationResult(strings.NewReader(""), 2)
			response := buildCommunicationMatchResponse(competitiveRequest(), processes, nil, protocolErr, 2, 10, &processes[0], canceledAt, nil)
			if response.Match.Outcome != "judge_error" || !response.Match.Retriable || response.Match.Wins != nil {
				t.Fatalf("manager failure entered cancellation exemption: %+v", response.Match)
			}
		})
	}
}

func TestCompetitiveManagerCancellationRequiresInterruptedProtocolAndNoOwnDeadline(t *testing.T) {
	for _, fixture := range []struct {
		name, protocol string
		protocolMs     int
		deadlineMs     int
		forfeit        bool
	}{
		{"empty after cancellation", "", 3, 0, true},
		{"truncated after cancellation", `{"version":1,"wins":[0,`, 3, 0, true},
		{"empty closed before cancellation", "", 1, 0, false},
		{"truncated closed before cancellation", `{"version":1,`, 1, 0, false},
		{"malformed complete payload", `{"version":oops}`, 3, 0, false},
		{"invalid completed counts", `{"version":1,"wins":[0,0],"games":20}`, 3, 0, false},
		{"trailing payload", `{"version":1,"wins":[0,20],"games":20}{`, 3, 0, false},
		{"unknown field", `{"version":1,"wins":[0,20],"games":20,"score":1}`, 3, 0, false},
		{"independent manager deadline", "", 3, 1, false},
		{"manager timer after cancellation", "", 3, 3, true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			at := time.Now()
			processes := competitiveProcesses(at)
			processes[0].result = execResult{Status: model.RunStatusRE, VerdictSource: "exit_code"}
			processes[2].result = execResult{Status: model.RunStatusTLE, VerdictSource: "wall_time"}
			processes[2].completedAt = at.Add(4 * time.Millisecond)
			processes[2].resultProtocolAt = at.Add(time.Duration(fixture.protocolMs) * time.Millisecond)
			if fixture.deadlineMs != 0 {
				processes[2].wallDeadlineAt = at.Add(time.Duration(fixture.deadlineMs) * time.Millisecond)
			}
			protocol, err := readCommunicationResult(strings.NewReader(fixture.protocol), 2)
			response := buildCommunicationMatchResponse(competitiveRequest(), processes, protocol.match, err, 2, 10, &processes[0], at.Add(2*time.Millisecond), nil)
			if fixture.forfeit {
				if response.Match.Outcome != "forfeit" || response.Match.Retriable || response.Match.Wins == nil || *response.Match.Wins != [2]int{0, 20} {
					t.Fatalf("coordinator-interrupted result lost genuine forfeit: %+v", response.Match)
				}
			} else if response.Match.Outcome != "judge_error" || !response.Match.Retriable || response.Match.Wins != nil {
				t.Fatalf("independent manager deadline or protocol error became forfeit: %+v", response.Match)
			}
		})
	}
}

func TestCompetitiveParticipantDeadlinesRemainIndependentAfterSlowAccounting(t *testing.T) {
	for _, deadlineBeforeCancellation := range []bool{true, false} {
		at := time.Now()
		processes := competitiveProcesses(at)
		processes[0].result = execResult{Status: model.RunStatusTLE, VerdictSource: "wall_time"}
		processes[0].wallDeadlineAt = at.Add(time.Millisecond)
		processes[0].completedAt = at.Add(2 * time.Millisecond)
		processes[1].result = execResult{Status: model.RunStatusTLE, VerdictSource: "wall_time"}
		processes[1].completedAt = at.Add(5 * time.Millisecond)
		processes[1].wallDeadlineAt = at.Add(4 * time.Millisecond)
		if deadlineBeforeCancellation {
			processes[1].wallDeadlineAt = at.Add(time.Millisecond)
		}
		processes[2].result = execResult{Status: model.RunStatusTLE, VerdictSource: "wall_time"}
		processes[2].completedAt = at.Add(5 * time.Millisecond)
		processes[2].resultProtocolAt = at.Add(5 * time.Millisecond)
		_, protocolErr := readCommunicationResult(strings.NewReader(""), 2)
		response := buildCommunicationMatchResponse(competitiveRequest(), processes, nil, protocolErr, 2, 10, &processes[0], at.Add(3*time.Millisecond), nil)
		if deadlineBeforeCancellation {
			if response.Match.Outcome != "judge_error" || response.VerdictSource != "communication:double_forfeit" || response.Match.Wins != nil {
				t.Fatalf("second independent wall deadline was masked by slow final accounting: %+v", response.Match)
			}
		} else if response.Match.Outcome != "forfeit" || response.Match.Wins == nil || *response.Match.Wins != [2]int{0, 20} {
			t.Fatalf("late timer hid genuine coordinator shutdown: %+v", response.Match)
		}
	}
}

func TestCompetitiveEarlierStreamFailureCannotHideOtherSeatFinalResourceFault(t *testing.T) {
	for _, resource := range []string{"cpu", "memory"} {
		for _, managerParsedBeforeStream := range []bool{true, false} {
			t.Run(resource, func(t *testing.T) {
				at := time.Now()
				processes := competitiveProcesses(at)
				processes[0].result = execResult{Status: model.RunStatusRE, VerdictSource: "stream_io"}
				processes[0].completedAt = at.Add(time.Millisecond)
				processes[1].result = execResult{Status: model.RunStatusTLE, VerdictSource: "wall_time"}
				processes[1].completedAt = at.Add(4 * time.Millisecond)
				if resource == "cpu" {
					processes[1].result.CPUTimeMs = 2001
				} else {
					processes[1].result.MemoryKB = 1024*1024 + 1
				}
				processes[2].completedAt = at.Add(2 * time.Millisecond)
				processes[2].resultProtocolAt = at.Add(2 * time.Millisecond)
				if managerParsedBeforeStream {
					processes[2].resultProtocolAt = at
				}
				result := &communicationMatchManagerResult{Version: 1, Wins: []int{7, 13}, Games: 20}
				response := buildCommunicationMatchResponse(competitiveRequest(), processes, result, nil, 2, 10, &processes[0], at.Add(3*time.Millisecond), nil)
				if managerParsedBeforeStream {
					if response.Match.Outcome != "forfeit" || response.Match.Wins == nil || *response.Match.Wins != [2]int{20, 0} {
						t.Fatalf("proven manager transport shutdown hid other seat's independent resource fault: %+v", response.Match)
					}
				} else if response.Match.Outcome != "judge_error" || response.Match.Wins != nil || response.VerdictSource != "communication:double_forfeit" {
					t.Fatalf("ambiguous earlier stream fault was discarded and awarded wins: %+v", response.Match)
				}
			})
		}
	}
}

func TestCompetitiveCanceledManagerWithCleanObservedExitKeepsForfeit(t *testing.T) {
	at := time.Now()
	processes := competitiveProcesses(at)
	processes[0].result = execResult{Status: model.RunStatusRE, VerdictSource: "exit_code"}
	zero := 0
	processes[2].result = execResult{Status: model.RunStatusTLE, VerdictSource: "wall_time", ExitCode: &zero}
	processes[2].completedAt = at.Add(2 * time.Millisecond)
	result := &communicationMatchManagerResult{Version: 1, Wins: []int{0, 20}, Games: 20}
	response := buildCommunicationMatchResponse(competitiveRequest(), processes, result, nil, 2, 10, &processes[0], at.Add(time.Millisecond), nil)
	if response.Match.Outcome != "forfeit" || response.Match.Wins == nil || *response.Match.Wins != [2]int{0, 20} {
		t.Fatalf("supervisor cancellation status erased observed clean manager exit and player forfeit: %+v", response.Match)
	}
}
