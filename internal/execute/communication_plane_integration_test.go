package execute

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

// This optional integration uses only the original five source-supplied
// fixtures. It creates diagnostic contestant code, never contest test data.
func TestCommunicationPlaneOriginalFixtures(t *testing.T) {
	adapterDir := os.Getenv("AONOHAKO_PLANE_ADAPTER_DIR")
	fixtureDir := os.Getenv("AONOHAKO_PLANE_FIXTURE_DIR")
	receiptPath := os.Getenv("AONOHAKO_PLANE_FIXTURE_RECEIPT")
	if adapterDir == "" || fixtureDir == "" || receiptPath == "" {
		t.Skip("set the official Plane adapter, fixture directory, and verified fixture receipt")
	}
	if os.Geteuid() != 0 {
		t.Skip("communication sandbox integration requires root")
	}
	forceDirectMode(t)
	receiptBytes, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		SourceCommit      string `json:"sourceCommit"`
		TestDataGenerated bool   `json:"testDataGenerated"`
		Files             []struct {
			InputName    string `json:"inputName"`
			InputSHA256  string `json:"inputSHA256"`
			OutputName   string `json:"outputName"`
			OutputSHA256 string `json:"outputSHA256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(receiptBytes, &receipt); err != nil || receipt.TestDataGenerated || len(receipt.Files) != 5 || receipt.SourceCommit != "af8e93c21ce3313e3f9a49caf5e5ccaf80a984b5" {
		t.Fatalf("invalid original fixture receipt: %v", err)
	}
	readVerified := func(name, expected string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(fixtureDir, name))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != expected {
			t.Fatalf("original fixture hash mismatch: %s", name)
		}
		return data
	}
	buildProgram := func(id, reference string) model.RunProgram {
		t.Helper()
		workDir := t.TempDir()
		files := []string{"manager.cpp"}
		if reference != "" {
			files = []string{"game.h", "stub.cpp", reference}
		}
		sourceHash := sha256.New()
		for _, name := range files {
			data, err := os.ReadFile(filepath.Join(adapterDir, name))
			if err != nil {
				t.Fatal(err)
			}
			if name == reference {
				// Assertions inside each independently compiled participant prove
				// the first-game last value and ten games in each turn role.
				needle := "void Init(int last,int id,vector<int> types,bool first) {"
				if strings.Count(string(data), needle) != 1 {
					t.Fatal("unexpected authored reference Init definition")
				}
				data = []byte(strings.Replace(string(data), needle, needle+`
    static int calls=0, first_count=0;
    if(calls==0 && last!=2) abort();
    ++calls; first_count+=first;
    if(calls==20 && first_count!=10) abort();
`, 1))
			}
			fmt.Fprintf(sourceHash, "%s\x00%d\x00", name, len(data))
			_, _ = sourceHash.Write(data)
			if err := os.WriteFile(filepath.Join(workDir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		binaryPath := filepath.Join(workDir, "program")
		args := []string{"-O2", "-std=c++17", "-o", binaryPath}
		if reference == "" {
			args = append(args, filepath.Join(workDir, "manager.cpp"))
		} else {
			args = append(args, filepath.Join(workDir, "stub.cpp"), filepath.Join(workDir, reference))
		}
		if output, err := exec.Command("c++", args...).CombinedOutput(); err != nil {
			t.Fatalf("compile %s: %v %s", id, err, output)
		}
		data, err := os.ReadFile(binaryPath)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		return model.RunProgram{ID: id, Lang: "binary", SourceSHA256: hex.EncodeToString(sourceHash.Sum(nil)), Binaries: []model.Binary{{Name: "main", Mode: "exec", DataB64: base64.StdEncoding.EncodeToString(data), SHA256: hex.EncodeToString(hash[:])}}}
	}
	forward := buildProgram("forward", "reference.cpp")
	reverse := buildProgram("reverse", "reference-reverse.cpp")
	manager := buildProgram("manager", "")
	if forward.Binaries[0].SHA256 == reverse.Binaries[0].SHA256 {
		t.Fatal("the two strategies must be independently compiled distinct programs")
	}
	cleanExitB64 := buildCTestBinary(t, "int main(void) { return 0; }")
	cleanExitData, err := base64.StdEncoding.DecodeString(cleanExitB64)
	if err != nil {
		t.Fatal(err)
	}
	cleanExitHash := sha256.Sum256(cleanExitData)
	cleanExit := model.RunProgram{ID: "clean-exit", Lang: "binary", SourceSHA256: hex.EncodeToString(cleanExitHash[:]), Binaries: []model.Binary{{Name: "main", Mode: "exec", DataB64: cleanExitB64, SHA256: hex.EncodeToString(cleanExitHash[:])}}}
	service := NewWithConfig(config.Config{
		CompetitiveRuntimeFingerprint: os.Getenv("AONOHAKO_COMPETITIVE_RUNTIME_FINGERPRINT"),
		CommunicationEnabled:          true, CommunicationMemoryBudgetMB: 3072,
		CommunicationCPUCount: 2, CommunicationWallBudgetMs: 600000, WorkRootMaxBytes: 1 << 30,
		Execution: config.ExecutionConfig{Platform: platform.RuntimeOptions{
			DeploymentTarget:   platform.DeploymentTargetCloudRun,
			ExecutionTransport: platform.ExecutionTransportEmbedded, SandboxBackend: platform.SandboxBackendHelper,
		}},
	})
	type matchReceipt struct {
		Fixture  string            `json:"fixture"`
		Mode     string            `json:"mode"`
		Response model.RunResponse `json:"response"`
	}
	var results []matchReceipt
	for _, fixture := range receipt.Files {
		input := readVerified(fixture.InputName, fixture.InputSHA256)
		answer := readVerified(fixture.OutputName, fixture.OutputSHA256)
		var replayWins [2]int
		for _, mode := range []string{"normal", "replay", "swapped", "early clean exit", "fixture digest mismatch"} {
			t.Run(fixture.InputName+"/"+mode, func(t *testing.T) {
				left, right := forward, reverse
				if mode == "swapped" {
					left, right = reverse, forward
				} else if mode == "early clean exit" {
					left = cleanExit
				}
				req := &model.RunRequest{
					Programs: []model.RunProgram{right, manager, left},
					Communication: &model.CommunicationSpec{
						Version: 2, ParticipantProgramIDs: []string{left.ID, right.ID}, ManagerProgramID: manager.ID,
						ParticipantCount: 2, ResultProtocol: "match-result-v1", Input: string(input), Answer: string(answer), MatchSeed: strings.Repeat("3", 64),
						InputSHA256:        fixture.InputSHA256,
						RuntimeFingerprint: service.competitiveRuntimeIdentity.RuntimeFingerprint,
					},
					Limits: model.Limits{TimeMs: 2000, MemoryMB: 1024, OutputBytes: 1 << 20, WorkspaceBytes: 8 << 20},
				}
				if mode == "fixture digest mismatch" {
					req.Communication.InputSHA256 = strings.Repeat("0", 64)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				response := service.Run(ctx, req, Hooks{})
				results = append(results, matchReceipt{fixture.InputName, mode, response})
				if mode == "fixture digest mismatch" {
					if response.Match == nil || response.Match.Outcome != "judge_error" || response.Match.Retriable || response.Match.ErrorCode != "input_integrity" || response.Match.Wins != nil || response.StartedParticipants != 0 {
						t.Fatalf("immutable fixture mismatch was not rejected before launch: %+v", response)
					}
					return
				}
				if response.Status != model.RunStatusAccepted || response.Score != nil || response.Match == nil || response.Match.Retriable || response.Match.Wins == nil {
					t.Fatalf("official match did not complete: %+v", response)
				}
				if response.Match.RuntimeFingerprint != service.competitiveRuntimeIdentity.RuntimeFingerprint || response.Match.InputSHA256 != fixture.InputSHA256 || response.Match.Participants[0].EntryPoint != "main" || response.Match.Participants[1].EntryPoint != "main" {
					t.Fatalf("resident runtime, fixture, or selected entry point binding missing: %+v", response.Match)
				}
				if mode == "early clean exit" {
					if *response.Match.Wins != [2]int{0, 20} {
						t.Fatalf("early EOF did not lose all games: %+v", response.Match)
					}
				} else if response.Match.Outcome != "completed" {
					t.Fatalf("valid strategies forfeited: %+v", response.Match)
				}
				if mode == "normal" {
					replayWins = *response.Match.Wins
				} else if mode == "replay" && replayWins != *response.Match.Wins {
					t.Fatalf("pinned manager seed did not replay exactly: %v != %v", replayWins, *response.Match.Wins)
				}
			})
		}
	}
	encoded, err := json.Marshal(struct {
		SourceCommit string         `json:"officialSourceCommit"`
		Results      []matchReceipt `json:"results"`
	}{receipt.SourceCommit, results})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("PLANE_MATCH_RECEIPT %s\n", encoded)
}
