package execute

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

// This exercises real communication pipes.
// Each participant first consumes a complete binary request and EOF, then sends
// a complete binary response and exits. Both transfers exceed pipe capacity.
func testCommunicationLargeStreamsAndEOF(t *testing.T) {
	t.Helper()
	enforce := os.Getenv("AONOHAKO_ENFORCE_SANDBOX_TESTS") != ""
	if os.Geteuid() != 0 {
		if enforce {
			t.Fatal("communication sandbox integration requires root")
		}
		t.Skip("communication sandbox integration requires root")
	}
	_, compilerErr := exec.LookPath("cc")
	if compilerErr != nil {
		_, compilerErr = exec.LookPath("gcc")
	}
	if compilerErr != nil {
		if enforce {
			t.Fatal("communication sandbox integration requires a C compiler")
		}
		t.Skip("communication sandbox integration requires a C compiler")
	}
	forceDirectMode(t)

	participantBinary := buildCTestBinary(t, `
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

#define PAYLOAD_BYTES (256U * 1024U)
static unsigned char payload[PAYLOAD_BYTES];

static unsigned char request_byte(size_t offset, unsigned int seat) {
  return (unsigned char)((offset * 37U + offset / 251U + seat * 17U) % 251U);
}

static int fail(int code) {
  fprintf(stderr, "large-stream participant failure=%d errno=%d\n", code, errno);
  return code;
}

int main(int argc, char **argv) {
  if (argc != 2) return fail(10);
  unsigned int seat = (unsigned int)strtoul(argv[1], NULL, 10);
  if (seat > 1) return fail(11);
  size_t received = 0;
  while (received < sizeof(payload)) {
    ssize_t n = read(STDIN_FILENO, payload + received, sizeof(payload) - received);
    if (n < 0 && errno == EINTR) continue;
    if (n <= 0) return fail(12);
    for (ssize_t i = 0; i < n; ++i) {
      if (payload[received + (size_t)i] != request_byte(received + (size_t)i, seat)) return fail(13);
    }
    received += (size_t)n;
  }
  unsigned char extra = 0;
  ssize_t n;
  do { n = read(STDIN_FILENO, &extra, 1); } while (n < 0 && errno == EINTR);
  if (n != 0) return fail(14);

  for (size_t i = 0; i < sizeof(payload); ++i) payload[i] ^= 0xa5U;
  size_t sent = 0;
  while (sent < sizeof(payload)) {
    n = write(STDOUT_FILENO, payload + sent, sizeof(payload) - sent);
    if (n < 0 && errno == EINTR) continue;
    if (n <= 0) return fail(15);
    sent += (size_t)n;
  }
  return 0;
}
`)
	managerBinary := buildCTestBinary(t, `
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#define PAYLOAD_BYTES (256U * 1024U)
static unsigned char payload[PAYLOAD_BYTES];

static unsigned char request_byte(size_t offset, unsigned int seat) {
  return (unsigned char)((offset * 37U + offset / 251U + seat * 17U) % 251U);
}

static int fail(int code) {
  fprintf(stderr, "large-stream manager failure=%d errno=%d\n", code, errno);
  return code;
}

static int pipe_fd(const char *path) {
  int fd = -1;
  char trailing;
  if (sscanf(path, "/proc/self/fd/%d%c", &fd, &trailing) != 1 || fd < 3) return -1;
  return fd;
}

static int write_all(int fd, const unsigned char *data, size_t size) {
  size_t sent = 0;
  while (sent < size) {
    ssize_t n = write(fd, data + sent, size - sent);
    if (n < 0 && errno == EINTR) continue;
    if (n <= 0) return -1;
    sent += (size_t)n;
  }
  return 0;
}

int main(int argc, char **argv) {
  if (argc != 9 || strcmp(argv[4], "2") != 0) return fail(20);
  for (unsigned int seat = 0; seat < 2; ++seat) {
    int participant_out = pipe_fd(argv[5 + seat * 2]);
    int participant_in = pipe_fd(argv[6 + seat * 2]);
    if (participant_out < 0 || participant_in < 0) return fail(21);
    int read_capacity = fcntl(participant_out, F_GETPIPE_SZ);
    int write_capacity = fcntl(participant_in, F_GETPIPE_SZ);
    if (read_capacity <= 0 || write_capacity <= 0 ||
        (size_t)read_capacity >= sizeof(payload) || (size_t)write_capacity >= sizeof(payload)) return fail(30);
    for (size_t i = 0; i < sizeof(payload); ++i) payload[i] = request_byte(i, seat);
    if (write_all(participant_in, payload, sizeof(payload)) != 0) return fail(22);
    // Close the inherited descriptor itself. Reopening its /proc path would
    // leave the original write end alive and would not deliver EOF here.
    if (close(participant_in) != 0) return fail(23);

    size_t received = 0;
    while (received < sizeof(payload)) {
      ssize_t n = read(participant_out, payload + received, sizeof(payload) - received);
      if (n < 0 && errno == EINTR) continue;
      if (n <= 0) return fail(24);
      for (ssize_t i = 0; i < n; ++i) {
        unsigned char expected = (unsigned char)(request_byte(received + (size_t)i, seat) ^ 0xa5U);
        if (payload[received + (size_t)i] != expected) return fail(25);
      }
      received += (size_t)n;
    }
    unsigned char extra = 0;
    ssize_t n;
    do { n = read(participant_out, &extra, 1); } while (n < 0 && errno == EINTR);
    if (n != 0) return fail(26);
    if (close(participant_out) != 0) return fail(27);
  }

  int result_fd = pipe_fd(argv[3]);
  const char *result = "{\"verdict\":\"accepted\",\"score\":1,\"message\":\"large-streams-and-eof-verified\"}";
  if (result_fd < 0 || write_all(result_fd, (const unsigned char *)result, strlen(result)) != 0) return fail(28);
  if (close(result_fd) != 0) return fail(29);
  return 0;
}
`)

	service := NewWithConfig(config.Config{
		CommunicationEnabled:        true,
		CommunicationMemoryBudgetMB: 1024,
		CommunicationCPUCount:       2,
		CommunicationWallBudgetMs:   30000,
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
		Limits: model.Limits{TimeMs: 3000, MemoryMB: 64, OutputBytes: 512 << 10, WorkspaceBytes: 8 << 20},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	response := service.Run(ctx, request, Hooks{})
	if response.Status != model.RunStatusAccepted || response.Score == nil || *response.Score != 1 ||
		response.StartedParticipants != 2 || response.Reason != "large-streams-and-eof-verified" {
		t.Fatalf("large binary streams or EOF failed: %+v", response)
	}
}
