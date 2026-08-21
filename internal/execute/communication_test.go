package execute

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

func TestReadCommunicationManagerResult(t *testing.T) {
	result, err := readCommunicationManagerResult(strings.NewReader(`{"verdict":"accepted","score":0.725,"message":""}`))
	if err != nil {
		t.Fatalf("readCommunicationManagerResult() error = %v", err)
	}
	if result.Score == nil || *result.Score != 0.725 || result.Verdict != "accepted" {
		t.Fatalf("unexpected manager result: %+v", result)
	}

	for _, input := range []string{
		`{"verdict":"accepted","message":""}`,
		`{"verdict":"accepted","score":1.1,"message":""}`,
		`{"verdict":"judge_error","score":0,"message":""}`,
		`{"verdict":"accepted","score":1,"extra":true}`,
		`{"verdict":"accepted","score":1}{"verdict":"accepted","score":1}`,
	} {
		if _, err := readCommunicationManagerResult(strings.NewReader(input)); err == nil {
			t.Fatalf("invalid manager result accepted: %s", input)
		}
	}
}

func TestCommunicationFailuresRemainExistingVerdicts(t *testing.T) {
	zeroExit := 0
	participantReq := &model.RunRequest{Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}}
	managerReq := &model.RunRequest{Limits: model.Limits{TimeMs: 2000, MemoryMB: 512}}
	base := []communicationProcessResult{
		{participant: 0, request: participantReq, result: execResult{Status: "OK", ExitCode: &zeroExit}},
		{participant: 1, request: participantReq, result: execResult{Status: "OK", ExitCode: &zeroExit}},
		{manager: true, participant: -1, request: managerReq, result: execResult{Status: "OK", ExitCode: &zeroExit}},
	}
	score := 0.725
	accepted := buildCommunicationResponse(base, communicationManagerResult{Verdict: "accepted", Score: &score}, nil, 2, 10, nil)
	if accepted.Status != model.RunStatusAccepted || accepted.Score == nil || *accepted.Score != score || accepted.StartedParticipants != 2 {
		t.Fatalf("unexpected accepted response: %+v", accepted)
	}
	fullScore := 1.0
	wrongAnswer := buildCommunicationResponse(base, communicationManagerResult{Verdict: "wrong_answer", Score: &fullScore}, nil, 2, 10, nil)
	if wrongAnswer.Status != model.RunStatusWA || wrongAnswer.Score == nil || *wrongAnswer.Score != fullScore {
		t.Fatalf("manager wrong_answer must remain WA even at full score: %+v", wrongAnswer)
	}

	managerFailed := append([]communicationProcessResult(nil), base...)
	managerFailed[2].result = execResult{Status: model.RunStatusRE, Reason: "boom", VerdictSource: "exit_code"}
	response := buildCommunicationResponse(managerFailed, communicationManagerResult{}, errors.New("missing result"), 2, 10, nil)
	if response.Status != model.RunStatusRE || !strings.HasPrefix(response.VerdictSource, "manager:") {
		t.Fatalf("manager failure must remain RE: %+v", response)
	}

	participantTimedOut := append([]communicationProcessResult(nil), base...)
	participantTimedOut[0].result = execResult{Status: model.RunStatusTLE, Reason: "timeout", VerdictSource: "wall_time"}
	timeoutFailure := participantTimedOut[0]
	response = buildCommunicationResponse(participantTimedOut, communicationManagerResult{Verdict: "accepted", Score: &score}, nil, 2, 10, &timeoutFailure)
	if response.Status != model.RunStatusTLE || !strings.HasPrefix(response.VerdictSource, "participant:0:") {
		t.Fatalf("participant timeout must remain TLE: %+v", response)
	}
}

func TestCommunicationResponsePreservesTriggeringParticipantFailure(t *testing.T) {
	zeroExit := 0
	participantReq := &model.RunRequest{Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}}
	managerReq := &model.RunRequest{Limits: model.Limits{TimeMs: 2000, MemoryMB: 512}}
	processes := []communicationProcessResult{
		{participant: 0, request: participantReq, result: execResult{Status: model.RunStatusTLE, Reason: "coordinator cancellation", VerdictSource: "wall_time"}},
		{participant: 63, request: participantReq, result: execResult{Status: model.RunStatusRE, Reason: "exit status 1", VerdictSource: "exit_code"}},
		{manager: true, participant: -1, request: managerReq, result: execResult{Status: "OK", ExitCode: &zeroExit}},
	}
	failure := processes[1]
	score := 1.0
	response := buildCommunicationResponse(processes, communicationManagerResult{Verdict: "accepted", Score: &score}, nil, 64, 10, &failure)
	if response.Status != model.RunStatusRE || !strings.HasPrefix(response.VerdictSource, "participant:63:") {
		t.Fatalf("triggering failure was overwritten by a cancellation artifact: %+v", response)
	}

	failure.result.Status = model.RunStatusInitFail
	response = buildCommunicationResponse(processes, communicationManagerResult{Verdict: "accepted", Score: &score}, nil, 64, 10, &failure)
	if response.Status != model.RunStatusRE {
		t.Fatalf("participant setup failures must be normalized to RE: %+v", response)
	}
}

func TestCommunicationResponseTrustsManagerAfterParticipantsAreStopped(t *testing.T) {
	zeroExit := 0
	completed := time.Now()
	participantReq := &model.RunRequest{Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}}
	managerReq := &model.RunRequest{Limits: model.Limits{TimeMs: 2000, MemoryMB: 512}}
	processes := []communicationProcessResult{
		{participant: 0, request: participantReq, result: execResult{Status: model.RunStatusRE, Reason: "broken pipe", VerdictSource: "stream_io"}, completedAt: completed.Add(time.Millisecond)},
		{manager: true, participant: -1, request: managerReq, result: execResult{Status: "OK", ExitCode: &zeroExit}, completedAt: completed},
	}
	score := 0.75
	response := buildCommunicationResponse(processes, communicationManagerResult{Verdict: "accepted", Score: &score}, nil, 1, 10, nil)
	if response.Status != model.RunStatusAccepted || response.Score == nil || *response.Score != score {
		t.Fatalf("manager verdict was overwritten by shutdown artifacts: %+v", response)
	}
}

func TestCommunicationOutputWriterCapsForwardedBytes(t *testing.T) {
	var forwarded bytes.Buffer
	cancelCount := 0
	writer := &communicationOutputWriter{
		target:    &forwarded,
		remaining: 4,
		onLimit:   func() { cancelCount++ },
	}

	for _, chunk := range [][]byte{[]byte("abc"), []byte("def"), []byte("ghi")} {
		if n, err := writer.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = (%d, %v)", chunk, n, err)
		}
	}
	if got := forwarded.String(); got != "abcd" {
		t.Fatalf("forwarded output = %q, want %q", got, "abcd")
	}
	if !writer.exceeded.Load() || cancelCount != 1 {
		t.Fatalf("limit state = %v, cancel count = %d", writer.exceeded.Load(), cancelCount)
	}
}

func TestCopyReadOnlyArtifactsUsesDistinctInodesAndLocks(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	artifact := filepath.Join(source, "Main")
	if err := os.WriteFile(artifact, []byte("binary"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := copyReadOnlyArtifacts(source, destination); err != nil {
		t.Fatalf("copyReadOnlyArtifacts() error = %v", err)
	}
	sourceInfo, _ := os.Stat(artifact)
	destinationPath := filepath.Join(destination, "Main")
	destinationInfo, _ := os.Stat(destinationPath)
	if sourceInfo.Sys().(*syscall.Stat_t).Ino == destinationInfo.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("participant artifacts share an inode")
	}
	if destinationInfo.Mode().Perm()&0o222 != 0 {
		t.Fatalf("participant artifact mode = %o, want no write bits", destinationInfo.Mode().Perm())
	}
	if got := destinationInfo.Sys().(*syscall.Stat_t).Uid; got != uint32(os.Geteuid()) {
		t.Fatalf("participant artifact uid = %d, want executor uid %d", got, os.Geteuid())
	}
	sourceFile, err := os.Open(artifact)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceFile.Close()
	destinationFile, err := os.Open(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer destinationFile.Close()
	if err := syscall.Flock(int(sourceFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(sourceFile.Fd()), syscall.LOCK_UN)
	if err := syscall.Flock(int(destinationFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("participant artifact lock leaked across copies: %v", err)
	}
}

func TestCopyReadOnlyArtifactsRejectsForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing artifact ownership requires root")
	}
	source := t.TempDir()
	destination := t.TempDir()
	artifact := filepath.Join(source, "Main")
	if err := os.WriteFile(artifact, []byte("binary"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(artifact, 1, -1); err != nil {
		t.Fatal(err)
	}
	err := copyReadOnlyArtifacts(source, destination)
	if err == nil || !strings.Contains(err.Error(), "not owned by executor uid") {
		t.Fatalf("copyReadOnlyArtifacts() error = %v, want ownership rejection", err)
	}
}

func TestCanonicalizeCommunicationExecutableIgnoresSubmittedRuntimeName(t *testing.T) {
	ws := Workspace{RootDir: t.TempDir()}
	ws.BoxDir = filepath.Join(ws.RootDir, "box")
	if err := os.MkdirAll(ws.BoxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	submitted := filepath.Join(ws.BoxDir, "aonohako-gleam-run")
	if err := os.WriteFile(submitted, []byte("binary"), 0o555); err != nil {
		t.Fatal(err)
	}
	args, err := canonicalizeCommunicationExecutable(ws, []string{submitted, "arg"}, "participant")
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(args[0]); got != ".aonohako-communication-participant" {
		t.Fatalf("canonical executable basename = %q", got)
	}
	if _, err := os.Stat(submitted); !os.IsNotExist(err) {
		t.Fatalf("submitted runtime name remains addressable: %v", err)
	}
}

func TestCommunicationCapabilitySupportsCloudRunAndSelfHostedCgroup(t *testing.T) {
	tests := []struct {
		name             string
		deploymentTarget platform.DeploymentTarget
		cgroupParent     string
		cloudEnabled     bool
		want             bool
	}{
		{name: "Cloud Run without cgroup", deploymentTarget: platform.DeploymentTargetCloudRun, cloudEnabled: true, want: true},
		{name: "Cloud Run without opt in", deploymentTarget: platform.DeploymentTargetCloudRun},
		{name: "Cloud Run with invalid cgroup", deploymentTarget: platform.DeploymentTargetCloudRun, cgroupParent: "/sys/fs/cgroup/aonohako"},
		{name: "self-hosted with cgroup", deploymentTarget: platform.DeploymentTargetSelfHosted, cgroupParent: "/sys/fs/cgroup/aonohako", want: true},
		{name: "self-hosted without cgroup", deploymentTarget: platform.DeploymentTargetSelfHosted},
		{name: "development", deploymentTarget: platform.DeploymentTargetDev},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := NewWithConfig(config.Config{CommunicationEnabled: tc.cloudEnabled, Execution: config.ExecutionConfig{
				Platform: platform.RuntimeOptions{DeploymentTarget: tc.deploymentTarget},
				Cgroup:   config.CgroupConfig{ParentDir: tc.cgroupParent},
			}})
			if got := service.supportsCommunicationV1(); got != tc.want {
				t.Fatalf("supportsCommunicationV1() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCreateCommunicationCgroupAllowsCloudRunWithoutParent(t *testing.T) {
	group, err := createCommunicationCgroup("", 64, model.Limits{MemoryMB: 64})
	if err != nil {
		t.Fatalf("createCommunicationCgroup() error = %v", err)
	}
	if group.Path != "" {
		t.Fatalf("createCommunicationCgroup() path = %q, want empty", group.Path)
	}
}

func TestCommunicationCloudRunWallAllowance(t *testing.T) {
	tests := []struct {
		name             string
		deploymentTarget platform.DeploymentTarget
		cgroupParent     string
		participantCount int
		availableCPUs    int
		timeLimitMs      int
		want             int64
		wantAllowed      bool
	}{
		{name: "64 participants reserve a supervisor CPU and slack", deploymentTarget: platform.DeploymentTargetCloudRun, participantCount: 64, availableCPUs: 8, timeLimitMs: 1000, want: 11500, wantAllowed: true},
		{name: "63 participants have no zero-margin wave", deploymentTarget: platform.DeploymentTargetCloudRun, participantCount: 63, availableCPUs: 8, timeLimitMs: 1000, want: 11500, wantAllowed: true},
		{name: "enough Cloud Run CPUs retain minimum slack", deploymentTarget: platform.DeploymentTargetCloudRun, participantCount: 64, availableCPUs: 128, timeLimitMs: 1000, want: 2000, wantAllowed: true},
		{name: "unknown Cloud Run CPU count fails closed", deploymentTarget: platform.DeploymentTargetCloudRun, participantCount: 2, timeLimitMs: 1000},
		{name: "self-hosted cgroup unchanged", deploymentTarget: platform.DeploymentTargetSelfHosted, cgroupParent: "/sys/fs/cgroup/aonohako", participantCount: 64, availableCPUs: 8, timeLimitMs: 1000, want: 1000, wantAllowed: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, allowed := communicationWallAllowanceMs(tc.deploymentTarget, tc.cgroupParent, tc.participantCount, tc.availableCPUs, tc.timeLimitMs)
			if got != tc.want || allowed != tc.wantAllowed {
				t.Fatalf("communicationWallAllowanceMs() = (%d, %v), want (%d, %v)", got, allowed, tc.want, tc.wantAllowed)
			}
		})
	}
}

func TestCommunicationMemoryWithinBudget(t *testing.T) {
	tests := []struct {
		name                string
		participants        int
		participantMemoryMB int
		budgetMB            int
		wantDeclared        int64
		want                bool
	}{
		{name: "64 participants admitted", participants: 64, participantMemoryMB: 256, budgetMB: 24576, wantDeclared: 16896, want: true},
		{name: "manager included", participants: 64, participantMemoryMB: 384, budgetMB: 24576, wantDeclared: 25088},
		{name: "exact boundary", participants: 2, participantMemoryMB: 256, budgetMB: 1024, wantDeclared: 1024, want: true},
		{name: "missing budget", participants: 2, participantMemoryMB: 256, wantDeclared: 0},
		{name: "zero participant memory", participants: 2, budgetMB: 1024, wantDeclared: 0},
		{name: "overflow", participants: 64, participantMemoryMB: int(^uint(0) >> 1), budgetMB: int(^uint(0) >> 1), wantDeclared: math.MaxInt64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			declared, got := communicationMemoryWithinBudget(tc.participants, tc.participantMemoryMB, tc.budgetMB)
			if declared != tc.wantDeclared || got != tc.want {
				t.Fatalf("communicationMemoryWithinBudget() = (%d, %v), want (%d, %v)", declared, got, tc.wantDeclared, tc.want)
			}
		})
	}
}

func TestCommunicationWorkspaceWithinBudget(t *testing.T) {
	declared, admitted := communicationWorkspaceWithinBudget(64, 8<<20, 128<<20, 1<<30)
	if !admitted || declared != 885837004 {
		t.Fatalf("communicationWorkspaceWithinBudget() = (%d, %v)", declared, admitted)
	}
	if _, admitted := communicationWorkspaceWithinBudget(64, 16<<20, 128<<20, 1<<30); admitted {
		t.Fatal("aggregate workspace overcommit was admitted")
	}
}

func TestCommunicationCloudRunRejectsDeclaredMemoryBeforeExecution(t *testing.T) {
	service := NewWithConfig(config.Config{
		CommunicationEnabled:        true,
		CommunicationMemoryBudgetMB: 16895,
		CommunicationCPUCount:       8,
		CommunicationWallBudgetMs:   600000,
		WorkRootMaxBytes:            1 << 30,
		Execution: config.ExecutionConfig{Platform: platform.RuntimeOptions{
			DeploymentTarget:   platform.DeploymentTargetCloudRun,
			ExecutionTransport: platform.ExecutionTransportEmbedded,
			SandboxBackend:     platform.SandboxBackendHelper,
		}},
	})
	req := &model.RunRequest{
		Programs: []model.RunProgram{
			{ID: "participant", Lang: "binary", Binaries: []model.Binary{{Name: "participant", DataB64: "eA==", Mode: "exec"}}},
			{ID: "manager", Lang: "binary", Binaries: []model.Binary{{Name: "manager", DataB64: "eA==", Mode: "exec"}}},
		},
		Communication: &model.CommunicationSpec{
			Version:              1,
			ParticipantProgramID: "participant",
			ManagerProgramID:     "manager",
			ParticipantCount:     64,
			ResultProtocol:       "manager-result-v1",
		},
		Limits: model.Limits{TimeMs: 1000, MemoryMB: 256},
	}
	response := service.Run(context.Background(), req, Hooks{})
	if response.Status != model.RunStatusRE || response.VerdictSource != "communication:admission" || response.StartedParticipants != 0 {
		t.Fatalf("unexpected admission response: %+v", response)
	}
}

func TestCommunicationCloudRunRejectsAggregateBudgetsBeforeExecution(t *testing.T) {
	request := &model.RunRequest{
		Programs: []model.RunProgram{
			{ID: "participant", Lang: "binary", Binaries: []model.Binary{{Name: "participant", DataB64: "eA==", Mode: "exec"}}},
			{ID: "manager", Lang: "binary", Binaries: []model.Binary{{Name: "manager", DataB64: "eA==", Mode: "exec"}}},
		},
		Communication: &model.CommunicationSpec{
			Version:              1,
			ParticipantProgramID: "participant",
			ManagerProgramID:     "manager",
			ParticipantCount:     64,
			ResultProtocol:       "manager-result-v1",
		},
		Limits: model.Limits{TimeMs: 1000, MemoryMB: 64},
	}
	tests := []struct {
		name               string
		workRootMaxBytes   int
		wallBudgetMs       int
		wantReasonContains string
	}{
		{name: "workspace", workRootMaxBytes: 512 << 20, wallBudgetMs: 600000, wantReasonContains: "workspace"},
		{name: "wall", workRootMaxBytes: 1 << 30, wallBudgetMs: 10000, wantReasonContains: "wall allowance"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := NewWithConfig(config.Config{
				CommunicationEnabled:        true,
				CommunicationMemoryBudgetMB: 24576,
				CommunicationCPUCount:       8,
				CommunicationWallBudgetMs:   tc.wallBudgetMs,
				WorkRootMaxBytes:            tc.workRootMaxBytes,
				Execution: config.ExecutionConfig{Platform: platform.RuntimeOptions{
					DeploymentTarget:   platform.DeploymentTargetCloudRun,
					ExecutionTransport: platform.ExecutionTransportEmbedded,
					SandboxBackend:     platform.SandboxBackendHelper,
				}},
			})
			response := service.Run(context.Background(), request, Hooks{})
			if response.Status != model.RunStatusRE || response.VerdictSource != "communication:admission" || response.StartedParticipants != 0 || !strings.Contains(response.Reason, tc.wantReasonContains) {
				t.Fatalf("unexpected admission response: %+v", response)
			}
		})
	}
}
