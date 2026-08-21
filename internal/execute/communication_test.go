package execute

import (
	"bytes"
	"errors"
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

func TestHardlinkReadOnlyArtifactsSharesInode(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	artifact := filepath.Join(source, "Main")
	if err := os.WriteFile(artifact, []byte("binary"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := hardlinkReadOnlyArtifacts(source, destination); err != nil {
		t.Fatalf("hardlinkReadOnlyArtifacts() error = %v", err)
	}
	sourceInfo, _ := os.Stat(artifact)
	destinationInfo, _ := os.Stat(filepath.Join(destination, "Main"))
	if sourceInfo.Sys().(*syscall.Stat_t).Ino != destinationInfo.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("participant artifact was copied instead of hard-linked")
	}
	if destinationInfo.Mode().Perm()&0o222 != 0 {
		t.Fatalf("participant artifact mode = %o, want no write bits", destinationInfo.Mode().Perm())
	}
	if got := destinationInfo.Sys().(*syscall.Stat_t).Uid; got != uint32(os.Geteuid()) {
		t.Fatalf("participant artifact uid = %d, want executor uid %d", got, os.Geteuid())
	}
}

func TestHardlinkReadOnlyArtifactsRejectsForeignOwner(t *testing.T) {
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
	err := hardlinkReadOnlyArtifacts(source, destination)
	if err == nil || !strings.Contains(err.Error(), "not owned by executor uid") {
		t.Fatalf("hardlinkReadOnlyArtifacts() error = %v, want ownership rejection", err)
	}
}

func TestCommunicationCapabilityRequiresSelfHostedCgroup(t *testing.T) {
	service := NewWithConfig(config.Config{Execution: config.ExecutionConfig{
		Platform: platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetSelfHosted},
		Cgroup:   config.CgroupConfig{ParentDir: "/sys/fs/cgroup/aonohako"},
	}})
	if !service.supportsCommunicationV1() {
		t.Fatal("self-hosted cgroup runner should support communication-v1")
	}
	service.deploymentTarget = platform.DeploymentTargetCloudRun
	if service.supportsCommunicationV1() {
		t.Fatal("Cloud Run must not advertise communication-v1")
	}
}
