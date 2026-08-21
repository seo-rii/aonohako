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
)

func TestCommunicationEndToEndWithDelegatedCgroup(t *testing.T) {
	participantPath := buildCTestBinary(t, `
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/stat.h>
#include <unistd.h>

int main(int argc, char **argv) {
  if (argc != 2) return 10;

  int artifact = open("shared.dat", O_WRONLY | O_TRUNC);
  if (artifact >= 0) {
    close(artifact);
    return 12;
  }
  if (chmod("shared.dat", 0600) == 0) return 13;
  FILE *shared = fopen("shared.dat", "r");
  if (!shared) return 14;
  char marker[10] = {0};
  if (fread(marker, 1, 9, shared) != 9) return 15;
  fclose(shared);
  if (marker[0] != 'i' || marker[8] != 'e') return 16;

  int id = atoi(argv[1]);
  int value = 0;
  if (scanf("%d", &value) != 1) return 11;
  printf("%d\n", id + value);
  fflush(stdout);
  return 0;
}
`)
	managerPath := buildCTestBinary(t, `
#include <stdio.h>
#include <stdlib.h>

int main(int argc, char **argv) {
  if (argc < 5) return 20;
  FILE *input = fopen(argv[1], "r");
  if (!input) return 21;
  int value = 0;
  if (fscanf(input, "%d", &value) != 1) return 22;
  fclose(input);

  int count = atoi(argv[4]);
  if (argc != 5 + count * 2) return 23;
  FILE **participantOut = calloc((size_t)count, sizeof(FILE *));
  FILE **participantIn = calloc((size_t)count, sizeof(FILE *));
  if (!participantOut || !participantIn) return 24;
  for (int id = 0; id < count; id++) {
    participantOut[id] = fopen(argv[5 + id * 2], "r");
    participantIn[id] = fopen(argv[6 + id * 2], "w");
    if (!participantOut[id] || !participantIn[id]) return 25;
  }
  for (int id = 0; id < count; id++) {
    fprintf(participantIn[id], "%d\n", value);
    fclose(participantIn[id]);
  }
  for (int id = 0; id < count; id++) {
    int response = -1;
    if (fscanf(participantOut[id], "%d", &response) != 1 || response != id + value) return 26;
    fclose(participantOut[id]);
  }
  free(participantOut);
  free(participantIn);

  FILE *result = fopen(argv[3], "w");
  if (!result) return 27;
  fputs("{\"verdict\":\"accepted\",\"score\":0.725,\"message\":\"\"}", result);
  fclose(result);
  return 0;
}
`)
	cgroupParent := strings.TrimSpace(os.Getenv("AONOHAKO_COMMUNICATION_TEST_CGROUP"))
	if cgroupParent == "" {
		t.Skip("set AONOHAKO_COMMUNICATION_TEST_CGROUP to a writable delegated cgroup v2 parent")
	}
	if os.Geteuid() != 0 {
		t.Skip("communication sandbox integration requires root")
	}
	forceDirectMode(t)

	participantBinary, err := os.ReadFile(participantPath)
	if err != nil {
		t.Fatal(err)
	}
	managerBinary, err := os.ReadFile(managerPath)
	if err != nil {
		t.Fatal(err)
	}

	service := NewWithConfig(config.Config{Execution: config.ExecutionConfig{
		Platform: platform.RuntimeOptions{
			DeploymentTarget:   platform.DeploymentTargetSelfHosted,
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
					Name:    "participant",
					DataB64: b64(string(participantBinary)),
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
					Name:    "manager",
					DataB64: b64(string(managerBinary)),
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
