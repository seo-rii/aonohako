package execute

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

//go:embed testdata/durante/communication.c
var duranteCommunicationSource []byte

func TestDuranteCommunicationAdmissionReportsUnstartedRoles(t *testing.T) {
	cfg, err := durante.ParseConfig("cpp", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithConfig(config.Config{
		CommunicationEnabled: true, CommunicationMemoryBudgetMB: 512,
		Execution: config.ExecutionConfig{
			WorkMeasurement: cfg,
			Platform: platform.RuntimeOptions{
				DeploymentTarget: platform.DeploymentTargetCloudRun, ExecutionTransport: platform.ExecutionTransportEmbedded, SandboxBackend: platform.SandboxBackendHelper,
			},
		},
	})
	req := &model.RunRequest{
		WorkMeasurement: true,
		Programs: []model.RunProgram{
			{ID: "participant", Lang: "C11", Binaries: []model.Binary{{Name: "main", DataB64: "eA==", Mode: "exec"}}},
			{ID: "manager", Lang: "C11", Binaries: []model.Binary{{Name: "main", DataB64: "eA==", Mode: "exec"}}},
		},
		Communication: &model.CommunicationSpec{Version: 1, ParticipantProgramID: "participant", ManagerProgramID: "manager", ParticipantCount: 2, ResultProtocol: "manager-result-v1"},
		Limits:        model.Limits{TimeMs: 3000, MemoryMB: 64},
	}
	response := service.Run(context.Background(), req, Hooks{})
	if response.Status != model.RunStatusRE || response.VerdictSource != "communication:admission" || !strings.Contains(response.Reason, "declared memory") || response.StartedParticipants != 0 {
		t.Fatalf("measurement changed the original admission failure: %+v", response)
	}
	if len(response.WorkMeasurements) != 3 {
		t.Fatalf("missing unstarted participant/manager identities: %+v", response)
	}
	for _, process := range response.WorkMeasurements {
		if process.ExecutionStatus != model.RunStatusInitFail || process.Report != nil || process.ExitCode != nil || process.TerminationSource != response.VerdictSource {
			t.Fatalf("unstarted role claimed an observation: %+v", process)
		}
	}
	if err := validateRemoteWorkMeasurement(req, response); err != nil {
		t.Fatalf("remote validation would hide the admission reason: %v", err)
	}
}

// The fixture is compiled in the build stage; the runtime needs no compiler.
func TestDuranteCommunicationIntegration(t *testing.T) {
	if os.Getenv("AONOHAKO_TEST_DURANTE") != "1" {
		t.Skip("set AONOHAKO_TEST_DURANTE=1 in the dedicated durante validation image")
	}
	if os.Geteuid() != 0 {
		t.Fatal("durante communication integration requires root")
	}
	fixture := os.Getenv("AONOHAKO_TEST_DURANTE_COMMUNICATION")
	if fixture == "" {
		t.Fatal("missing precompiled communication fixture")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	forceDirectMode(t)
	cfg, err := durante.ParseConfig("cpp", os.Getenv("AONOHAKO_DURANTE_DRRUN"), os.Getenv("AONOHAKO_DURANTE_CLIENT"), os.Getenv("AONOHAKO_DURANTE_DISPATCH_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewWithConfig(config.Config{
		CompetitiveRuntimeFingerprint: os.Getenv("AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT"),
		CommunicationEnabled:          true, CommunicationMemoryBudgetMB: 2048,
		CommunicationCPUCount: 2, CommunicationWallBudgetMs: 60000, WorkRootMaxBytes: 1 << 30,
		Execution: config.ExecutionConfig{
			WorkMeasurement: cfg,
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetCloudRun,
				ExecutionTransport: platform.ExecutionTransportEmbedded,
				SandboxBackend:     platform.SandboxBackendHelper,
			},
		},
	})
	if service.competitiveRuntimeIdentity.RuntimeFingerprint == "" {
		t.Fatal("communication-v2 requires the actual immutable test image digest")
	}
	artifact := func(name string, contents []byte, mode string) model.Binary {
		digest := sha256.Sum256(contents)
		return model.Binary{Name: name, DataB64: base64.StdEncoding.EncodeToString(contents), Mode: mode, SHA256: hex.EncodeToString(digest[:])}
	}
	sourceDigest := sha256.Sum256(duranteCommunicationSource)
	program := func(id, mode, marker string) model.RunProgram {
		return model.RunProgram{
			ID: id, Lang: "C11", SourceSHA256: hex.EncodeToString(sourceDigest[:]),
			Binaries: []model.Binary{
				artifact("main", data, "exec"),
				artifact("mode.dat", []byte(mode), ""),
				artifact("marker.dat", []byte(marker), ""),
			},
		}
	}
	request := func(version int, participant0Mode, participant1Mode, managerMode string) *model.RunRequest {
		req := &model.RunRequest{
			WorkMeasurement: true,
			Programs: []model.RunProgram{
				program("participant", participant0Mode, "shared"), program("manager", managerMode, "manager"),
			},
			Communication: &model.CommunicationSpec{
				Version: 1, ParticipantProgramID: "participant", ManagerProgramID: "manager",
				ParticipantCount: 2, ResultProtocol: "manager-result-v1",
			},
			Limits: model.Limits{TimeMs: 3000, MemoryMB: 512, OutputBytes: 512 << 10, WorkspaceBytes: 8 << 20},
		}
		if version == 2 {
			req.Programs = []model.RunProgram{program("seat0", participant0Mode, "seat0"), program("seat1", participant1Mode, "seat1"), program("manager", managerMode, "manager")}
			emptyDigest := sha256.Sum256(nil)
			req.Communication = &model.CommunicationSpec{
				Version: 2, ParticipantProgramIDs: []string{"seat0", "seat1"}, ManagerProgramID: "manager",
				ParticipantCount: 2, ResultProtocol: "match-result-v1", MatchSeed: strings.Repeat("3", 64),
				InputSHA256: hex.EncodeToString(emptyDigest[:]), RuntimeFingerprint: service.competitiveRuntimeIdentity.RuntimeFingerprint,
			}
		}
		return req
	}
	run := func(t *testing.T, req *model.RunRequest) model.RunResponse {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return service.Run(ctx, req, Hooks{})
	}
	checkReports := func(t *testing.T, req *model.RunRequest, response model.RunResponse) {
		t.Helper()
		if response.WorkMeasurement != nil || response.WorkMeasurementExecution != nil || len(response.WorkMeasurements) != 3 {
			t.Fatalf("missing separate participant/manager observations: %+v", response)
		}
		for i, process := range response.WorkMeasurements {
			role, id := "participant", "participant"
			if i == 2 {
				role, id = "manager", "manager"
			} else if req.Communication.Version == 2 {
				id = fmt.Sprintf("seat%d", i)
			}
			if process.Role != role || process.ProgramID != id || (i < 2 && (process.ParticipantIndex == nil || *process.ParticipantIndex != i)) || (i == 2 && process.ParticipantIndex != nil) {
				t.Fatalf("observation %d has wrong role identity: %+v", i, process)
			}
			if process.Report == nil || process.Report.Family != "cpp" || !process.Report.ValidDiagnosticContract() || !process.Report.ValidInstallationIdentity() {
				t.Fatalf("observation %d has invalid diagnostic contract: %+v", i, process)
			}
			if process.Report.Complete {
				if !validDiagnosticWorkReport("C11", process.Report) || process.Report.Processes != 1 {
					t.Fatalf("observation %d is not a complete independent process: %+v", i, process)
				}
			} else if !process.Report.ValidUnavailableResult() {
				t.Fatalf("observation %d exposed incomplete totals: %+v", i, process)
			}
		}
		if err := validateRemoteWorkMeasurement(req, response); err != nil {
			t.Fatalf("local role reports violate the remote contract: %v; %+v", err, response)
		}
	}
	checkComplete := func(t *testing.T, response model.RunResponse) {
		t.Helper()
		for i, process := range response.WorkMeasurements {
			if !process.Report.Complete || process.ExecutionStatus != "OK" || process.ExitCode == nil || *process.ExitCode != 0 {
				t.Fatalf("role %d did not complete normally: %+v", i, process)
			}
		}
	}

	t.Run("v1_streams_and_eof", func(t *testing.T) {
		req := request(1, "stream", "stream", "stream")
		response := run(t, req)
		if response.Status != model.RunStatusAccepted || response.Score == nil || *response.Score != 1 || response.StartedParticipants != 2 || response.Reason != "durante streams and EOF" {
			t.Fatalf("instrumented v1 stream exchange failed: %+v", response)
		}
		checkReports(t, req, response)
		checkComplete(t, response)
	})
	t.Run("v1_unmetered", func(t *testing.T) {
		req := request(1, "stream", "stream", "stream")
		req.WorkMeasurement = false
		response := run(t, req)
		if response.Status != model.RunStatusAccepted || response.WorkMeasurement != nil || len(response.WorkMeasurements) != 0 {
			t.Fatalf("unmetered communication changed: %+v", response)
		}
	})
	t.Run("v2_distinct_roles", func(t *testing.T) {
		req := request(2, "stream", "stream", "stream")
		response := run(t, req)
		if response.Status != model.RunStatusAccepted || response.Score != nil || response.Match == nil || response.Match.Outcome != "completed" || response.Match.Wins == nil || *response.Match.Wins != [2]int{7, 13} {
			t.Fatalf("instrumented v2 match failed: %+v", response)
		}
		checkReports(t, req, response)
		checkComplete(t, response)
	})
	t.Run("v2_forfeit", func(t *testing.T) {
		req := request(2, "exit", "wait", "forfeit")
		response := run(t, req)
		if response.Status != model.RunStatusAccepted || response.Match == nil || response.Match.Outcome != "forfeit" || response.Match.Wins == nil || *response.Match.Wins != [2]int{0, 20} {
			t.Fatalf("instrumentation changed a participant forfeit: %+v", response)
		}
		checkReports(t, req, response)
		failed := response.WorkMeasurements[0]
		if failed.ExitCode == nil || *failed.ExitCode != 17 || !failed.Report.Complete {
			t.Fatalf("normally exited losing seat lost its diagnostic observation: %+v", failed)
		}
		if response.WorkMeasurements[1].Report.Complete {
			t.Fatal("opponent shutdown after forfeit produced complete totals")
		}
	})
	for _, mode := range []string{"error", "invalid"} {
		t.Run("v2_manager_"+mode, func(t *testing.T) {
			req := request(2, "wait", "wait", mode)
			response := run(t, req)
			if response.Match == nil || response.Match.Outcome != "judge_error" || response.Match.Wins != nil || !response.Match.Retriable {
				t.Fatalf("manager failure became a scored match: %+v", response)
			}
			checkReports(t, req, response)
			if !response.WorkMeasurements[2].Report.Complete {
				t.Fatalf("normally exited manager lost its diagnostic report: %+v", response.WorkMeasurements[2])
			}
		})
	}
	t.Run("v2_manager_shutdown_is_not_complete_measurement", func(t *testing.T) {
		req := request(2, "wait", "wait", "early")
		response := run(t, req)
		if response.Match == nil || response.Match.Outcome != "completed" {
			t.Fatalf("manager-directed completion failed: %+v", response)
		}
		checkReports(t, req, response)
		for i := 0; i < 2; i++ {
			if response.Match.Participants[i].Status != "OK" || response.WorkMeasurements[i].Report.Complete {
				t.Fatalf("seat %d verdict reconciliation promoted stopped counters: %+v", i, response)
			}
		}
		if !response.WorkMeasurements[2].Report.Complete {
			t.Fatal("normally exited manager measurement is unavailable")
		}
	})
	t.Run("v1_cpu_limit", func(t *testing.T) {
		req := request(1, "burn", "burn", "wait")
		req.Limits.TimeMs = 100
		response := run(t, req)
		if response.Status != model.RunStatusTLE {
			t.Fatalf("instrumented participant CPU limit was not enforced: %+v", response)
		}
		checkReports(t, req, response)
		for _, process := range response.WorkMeasurements {
			if process.Report.Complete {
				t.Fatalf("resource/coordinator stop exposed complete counters: %+v", process)
			}
		}
	})
	t.Run("v2_cancel_reaps_all_roles", func(t *testing.T) {
		readyDir, err := os.MkdirTemp("/tmp", "durante-communication-ready-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(readyDir) })
		if err := os.Chmod(readyDir, 0o777); err != nil {
			t.Fatal(err)
		}
		readyPath := filepath.Join(readyDir, "ready")
		req := request(2, "wait", "wait", "wait")
		req.Communication.Input = readyPath
		digest := sha256.Sum256([]byte(readyPath))
		req.Communication.InputSHA256 = hex.EncodeToString(digest[:])
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan model.RunResponse, 1)
		go func() { result <- service.Run(ctx, req, Hooks{}) }()
		var pids [3]int
		deadline := time.Now().Add(10 * time.Second)
		for {
			ready, err := os.ReadFile(readyPath)
			if err == nil {
				if n, _ := fmt.Sscanf(string(ready), "%d %d %d", &pids[0], &pids[1], &pids[2]); n == 3 {
					break
				}
			}
			select {
			case response := <-result:
				t.Fatalf("communication returned before all roles were ready: %+v", response)
			default:
			}
			if time.Now().After(deadline) {
				cancel()
				response := <-result
				t.Fatalf("communication fixture did not reach running state: %+v", response)
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		var response model.RunResponse
		select {
		case response = <-result:
		case <-time.After(10 * time.Second):
			t.Fatal("communication cancellation did not finish")
		}
		if response.Match == nil || response.Match.Outcome != "judge_error" || response.Match.Wins != nil || response.VerdictSource != "communication:request_canceled" {
			t.Fatalf("request cancellation produced a match result: %+v", response)
		}
		checkReports(t, req, response)
		for i, process := range response.WorkMeasurements {
			if process.Report.Complete {
				t.Fatalf("canceled role %d returned complete totals", i)
			}
			if pids[i] <= 0 || syscall.Kill(pids[i], 0) != syscall.ESRCH {
				t.Fatalf("role %d process %d survived cancellation", i, pids[i])
			}
		}
	})
}
