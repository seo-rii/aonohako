package execute

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

func TestCommunicationV2DistinctProgramsAndForfeits(t *testing.T) {
	if os.Getenv("AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT") == "" {
		t.Skip("set the actual immutable test image digest for communication-v2")
	}
	if os.Geteuid() != 0 {
		t.Skip("communication sandbox integration requires root")
	}
	forceDirectMode(t)
	participant0 := buildCTestBinary(t, `
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
int main(int argc, char **argv) {
  if (argc != 2 || atoi(argv[1]) != 0 || getuid() != 65000) return 11;
  FILE *marker = fopen("marker.dat", "r");
  char c = 0;
  if (!marker || fscanf(marker, "%c", &c) != 1 || c != 'A') return 12;
  int value = 0;
  if (scanf("%d", &value) != 1) return 13;
  printf("%d\n", value + 7); fflush(stdout);
  return 0;
}`)
	participant1 := buildCTestBinary(t, `
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
int main(int argc, char **argv) {
  if (argc != 2 || atoi(argv[1]) != 1 || getuid() != 64999) return 21;
  FILE *marker = fopen("marker.dat", "r");
  char c = 0;
  if (!marker || fscanf(marker, "%c", &c) != 1 || c != 'B') return 22;
  int value = 0;
  if (scanf("%d", &value) != 1) return 23;
  printf("%d\n", value + 13); fflush(stdout);
  return 0;
}`)
	manager := buildCTestBinary(t, `
#include <stdio.h>
#include <string.h>
#include <signal.h>
int main(int argc, char **argv) {
  signal(SIGPIPE, SIG_IGN);
  if (argc != 10 || strcmp(argv[4], "2") != 0 || strlen(argv[9]) != 64 || argv[9][0] != '3') return 31;
  FILE *in0 = fopen(argv[6], "w"), *in1 = fopen(argv[8], "w");
  FILE *out0 = fopen(argv[5], "r"), *out1 = fopen(argv[7], "r");
  if (!in0 || !in1 || !out0 || !out1) return 32;
  fputs("10\n", in0); fflush(in0);
  fputs("20\n", in1); fflush(in1);
  int a = 0, b = 0;
  int seat0_failed = fscanf(out0, "%d", &a) != 1;
  if (!seat0_failed && (fscanf(out1, "%d", &b) != 1 || a != 17 || b != 33)) return 33;
  FILE *result = fopen(argv[3], "w");
  if (!result) return 34;
  fputs(seat0_failed ? "{\"version\":1,\"wins\":[0,20],\"games\":20}" : "{\"version\":1,\"wins\":[7,13],\"games\":20}", result);
  fclose(result);
  return 0;
}`)
	crasher := buildCTestBinary(t, `int main(void) { return 17; }`)
	managerCrash := buildCTestBinary(t, `int main(void) { return 29; }`)
	service := NewWithConfig(config.Config{
		CompetitiveRuntimeFingerprint: os.Getenv("AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT"),
		CommunicationEnabled:          true, CommunicationMemoryBudgetMB: 3072,
		CommunicationCPUCount: 2, CommunicationWallBudgetMs: 600000, WorkRootMaxBytes: 1 << 30,
		Execution: config.ExecutionConfig{Platform: platform.RuntimeOptions{
			DeploymentTarget:   platform.DeploymentTargetCloudRun,
			ExecutionTransport: platform.ExecutionTransportEmbedded, SandboxBackend: platform.SandboxBackendHelper,
		}},
	})
	program := func(id, encoded, marker string) model.RunProgram {
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		binaries := []model.Binary{{Name: "main", DataB64: encoded, Mode: "exec", SHA256: hex.EncodeToString(hash[:])}}
		if marker != "" {
			markerHash := sha256.Sum256([]byte(marker))
			binaries = append(binaries, model.Binary{Name: "marker.dat", DataB64: b64(marker), SHA256: hex.EncodeToString(markerHash[:])})
		}
		return model.RunProgram{ID: id, Lang: "binary", SourceSHA256: hex.EncodeToString(hash[:]), Binaries: binaries}
	}
	for _, mode := range []string{"normal", "seat0 crash", "manager crash"} {
		t.Run(mode, func(t *testing.T) {
			req := &model.RunRequest{
				Programs: []model.RunProgram{program("seat0", participant0, "A"), program("seat1", participant1, "B"), program("manager", manager, "")},
				Communication: &model.CommunicationSpec{
					Version: 2, ParticipantProgramIDs: []string{"seat0", "seat1"}, ManagerProgramID: "manager",
					ParticipantCount: 2, ResultProtocol: "match-result-v1", MatchSeed: strings.Repeat("3", 64),
				},
				Limits: model.Limits{TimeMs: 2000, MemoryMB: 1024, OutputBytes: 1024, WorkspaceBytes: 8 << 20},
			}
			emptyHash := sha256.Sum256(nil)
			req.Communication.InputSHA256 = hex.EncodeToString(emptyHash[:])
			req.Communication.RuntimeFingerprint = service.competitiveRuntimeIdentity.RuntimeFingerprint
			if mode == "seat0 crash" {
				req.Programs[0] = program("seat0", crasher, "")
			} else if mode == "manager crash" {
				req.Programs[2] = program("manager", managerCrash, "")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			response := service.Run(ctx, req, Hooks{})
			if response.Match == nil || response.Score != nil {
				t.Fatalf("missing match or synthetic score: %+v", response)
			}
			switch mode {
			case "normal":
				if response.Match.Outcome != "completed" || response.Match.Retriable || *response.Match.Wins != [2]int{7, 13} {
					t.Fatalf("different programs were not executed faithfully: %+v", response)
				}
			case "seat0 crash":
				if response.Match.Outcome != "forfeit" || response.Match.Retriable || *response.Match.Wins != [2]int{0, 20} {
					t.Fatalf("seat fault did not preserve opponent winner: %+v", response)
				}
			case "manager crash":
				if response.Match.Outcome != "judge_error" || !response.Match.Retriable || response.Match.Wins != nil {
					t.Fatalf("manager infrastructure fault was scored: %+v", response)
				}
			}
		})
	}
}
