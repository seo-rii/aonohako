package execute

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/security"
)

func TestCommunicationEndToEndWithDelegatedCgroup(t *testing.T) {
	cgroupParent := strings.TrimSpace(os.Getenv("AONOHAKO_COMMUNICATION_TEST_CGROUP"))
	testCommunicationEndToEnd(t, platform.DeploymentTargetSelfHosted, cgroupParent, true)
}

func TestCommunicationEndToEndWithoutCgroup(t *testing.T) {
	testCommunicationEndToEnd(t, platform.DeploymentTargetCloudRun, "", false)
}

func testCommunicationEndToEnd(t *testing.T, deploymentTarget platform.DeploymentTarget, cgroupParent string, requireCgroup bool) {
	t.Helper()
	participantBinary := buildCTestBinary(t, `
#include <fcntl.h>
#include <errno.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/stat.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <unistd.h>

static int fail_participant(int code) {
  fprintf(stderr, "participant failure code=%d errno=%d\n", code, errno);
  return code;
}

static void *thread_main(void *unused) {
  (void)unused;
  return NULL;
}

int main(int argc, char **argv) {
  if (argc != 2) return fail_participant(10);

  int artifact = open("shared.dat", O_WRONLY | O_TRUNC);
  if (artifact >= 0) {
    close(artifact);
    return fail_participant(12);
  }
  if (chmod("shared.dat", 0600) == 0) return fail_participant(13);
  FILE *shared = fopen("shared.dat", "r");
  if (!shared) return fail_participant(14);
  char marker[10] = {0};
  if (fread(marker, 1, 9, shared) != 9) return fail_participant(15);
  fclose(shared);
  if (marker[0] != 'i' || marker[8] != 'e') return fail_participant(16);
  errno = 0;
  if (fork() >= 0 || (errno != EPERM && errno != ENOSYS)) return fail_participant(17);
  errno = 0;
  if (socket(AF_UNIX, SOCK_STREAM, 0) >= 0 || errno != EPERM) return fail_participant(18);
#ifdef SYS_memfd_create
  errno = 0;
  if (syscall(SYS_memfd_create, "communication", 0) >= 0 || errno != EPERM) return fail_participant(19);
#endif
	pthread_t thread;
	if (pthread_create(&thread, NULL, thread_main, NULL) == 0) return fail_participant(29);

  int id = atoi(argv[1]);
  int value = 0;
  if (scanf("%d", &value) != 1) return fail_participant(11);
  printf("%d %d\n", id + value, (int)getuid());
  fflush(stdout);
  return 0;
}
`, "-pthread")
	managerBinary := buildCTestBinary(t, `
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

static int fail_manager(int code) {
  fprintf(stderr, "manager failure code=%d\n", code);
  return code;
}

int main(int argc, char **argv) {
  if (argc < 5) return fail_manager(20);
  FILE *input = fopen(argv[1], "r");
  if (!input) return fail_manager(21);
  int value = 0;
  if (fscanf(input, "%d", &value) != 1) return fail_manager(22);
  fclose(input);

  int count = atoi(argv[4]);
  if (argc != 5 + count * 2) return fail_manager(23);
  FILE **participantOut = calloc((size_t)count, sizeof(FILE *));
  FILE **participantIn = calloc((size_t)count, sizeof(FILE *));
  if (!participantOut || !participantIn) return fail_manager(24);
  for (int id = 0; id < count; id++) {
    participantOut[id] = fopen(argv[5 + id * 2], "r");
    participantIn[id] = fopen(argv[6 + id * 2], "w");
    if (!participantOut[id] || !participantIn[id]) {
      fprintf(stderr, "manager pipe id=%d read=%s write=%s errno=%d\n", id, argv[5 + id * 2], argv[6 + id * 2], errno);
      return fail_manager(25);
    }
  }
  for (int id = 0; id < count; id++) {
    fprintf(participantIn[id], "%d\n", value);
    fclose(participantIn[id]);
  }
  for (int id = 0; id < count; id++) {
    int response = -1;
    int uid = -1;
    if (fscanf(participantOut[id], "%d %d", &response, &uid) != 2 || response != id + value || uid != 65000 - id) return fail_manager(26);
    fclose(participantOut[id]);
  }
  free(participantOut);
  free(participantIn);

  FILE *result = fopen(argv[3], "w");
  if (!result) return fail_manager(27);
  fputs("{\"verdict\":\"accepted\",\"score\":0.725,\"message\":\"\"}", result);
  fclose(result);
  return 0;
}
`)
	if requireCgroup && cgroupParent == "" {
		t.Skip("set AONOHAKO_COMMUNICATION_TEST_CGROUP to a writable delegated cgroup v2 parent")
	}
	if os.Geteuid() != 0 {
		t.Skip("communication sandbox integration requires root")
	}
	if deploymentTarget == platform.DeploymentTargetCloudRun {
		if err := security.ValidateCommunicationIdentityReservation(); err != nil {
			t.Fatalf("Cloud Run communication identity reservation: %v", err)
		}
	}
	forceDirectMode(t)

	service := NewWithConfig(config.Config{
		CommunicationEnabled:        deploymentTarget == platform.DeploymentTargetCloudRun,
		CommunicationMemoryBudgetMB: 24576,
		CommunicationCPUCount:       8,
		CommunicationWallBudgetMs:   600000,
		WorkRootMaxBytes:            1 << 30,
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   deploymentTarget,
				ExecutionTransport: platform.ExecutionTransportEmbedded,
				SandboxBackend:     platform.SandboxBackendHelper,
			},
			Cgroup: config.CgroupConfig{ParentDir: cgroupParent},
		}})
	request := &model.RunRequest{
		Programs: []model.RunProgram{
			{
				ID:   "participant",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "aonohako-gleam-run",
					DataB64: participantBinary,
					Mode:    "exec",
				}, {
					Name:    "shared.dat",
					DataB64: b64("immutable"),
				}},
			},
			{
				ID:   "manager",
				Lang: "binary",
				Binaries: []model.Binary{{
					Name:    "aonohako-tla-run",
					DataB64: managerBinary,
					Mode:    "exec",
				}},
			},
		},
		Communication: &model.CommunicationSpec{
			Version:              1,
			ParticipantProgramID: "participant",
			ManagerProgramID:     "manager",
			ParticipantCount:     64,
			ResultProtocol:       "manager-result-v1",
			Input:                "7\n",
		},
		Limits: model.Limits{
			TimeMs:         3000,
			MemoryMB:       64,
			OutputBytes:    1024,
			WorkspaceBytes: 8 << 20,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	response := service.Run(ctx, request, Hooks{})
	if response.Status != model.RunStatusAccepted ||
		response.Score == nil || *response.Score != 0.725 ||
		response.StartedParticipants != 64 {
		t.Fatalf("unexpected communication response: %+v", response)
	}
}

func TestCommunicationWithoutCgroupCancellationReapsParticipants(t *testing.T) {
	participantBinary := buildCTestBinary(t, `
#include <stdio.h>
#include <unistd.h>

int main(void) {
  printf("%d\n", (int)getpid());
  fflush(stdout);
  for (;;) pause();
}
`)
	managerBinary := buildCTestBinary(t, `
#include <string.h>
#include <unistd.h>

int main(void) {
  const char *result = "{\"verdict\":\"accepted\",\"score\":1,\"message\":\"\"}";
  if (write(10, result, strlen(result)) < 0) return 23;
  return 0;
}
`)
	if os.Geteuid() != 0 {
		t.Skip("communication sandbox integration requires root")
	}
	if err := security.ValidateCommunicationIdentityReservationAt("/etc/passwd", "/etc/group", "/proc"); err != nil {
		t.Fatalf("reserved communication identity before run: %v", err)
	}
	forceDirectMode(t)

	service := NewWithConfig(config.Config{
		CommunicationEnabled:        true,
		CommunicationMemoryBudgetMB: 24576,
		CommunicationCPUCount:       8,
		CommunicationWallBudgetMs:   600000,
		WorkRootMaxBytes:            1 << 30,
		Execution: config.ExecutionConfig{Platform: platform.RuntimeOptions{
			DeploymentTarget:   platform.DeploymentTargetCloudRun,
			ExecutionTransport: platform.ExecutionTransportEmbedded,
			SandboxBackend:     platform.SandboxBackendHelper,
		}},
	})
	request := &model.RunRequest{
		Programs: []model.RunProgram{
			{ID: "participant", Lang: "binary", Binaries: []model.Binary{{Name: "participant", DataB64: participantBinary, Mode: "exec"}}},
			{ID: "manager", Lang: "binary", Binaries: []model.Binary{{Name: "manager", DataB64: managerBinary, Mode: "exec"}}},
		},
		Communication: &model.CommunicationSpec{
			Version:              1,
			ParticipantProgramID: "participant",
			ManagerProgramID:     "manager",
			ParticipantCount:     2,
			ResultProtocol:       "manager-result-v1",
		},
		Limits: model.Limits{TimeMs: 3000, MemoryMB: 64, OutputBytes: 1024, WorkspaceBytes: 8 << 20},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response := service.Run(ctx, request, Hooks{})
	if response.Status != model.RunStatusAccepted || response.StartedParticipants != 2 {
		t.Fatalf("unexpected communication response: %+v", response)
	}
	deadline := time.Now().Add(time.Second)
	for {
		err := security.ValidateCommunicationIdentityReservationAt("/etc/passwd", "/etc/group", "/proc")
		if err == nil {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("reserved communication process remained after cancellation: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
