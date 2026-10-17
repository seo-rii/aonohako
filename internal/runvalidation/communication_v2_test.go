package runvalidation

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func validCommunicationV2Request() *model.RunRequest {
	req := validCommunicationRequest()
	req.Communication = &model.CommunicationSpec{
		Version: 2, ParticipantProgramIDs: []string{"seat0", "seat1"}, ManagerProgramID: "manager",
		ParticipantCount: 2, ResultProtocol: "match-result-v1", MatchSeed: strings.Repeat("1", 64),
	}
	emptyHash := sha256.Sum256(nil)
	req.Communication.InputSHA256 = hex.EncodeToString(emptyHash[:])
	req.Communication.RuntimeFingerprint = strings.Repeat("a", 64)
	hash := sha256.Sum256([]byte("x"))
	req.Programs = nil
	for _, id := range []string{"manager", "seat1", "seat0"} {
		req.Programs = append(req.Programs, model.RunProgram{
			ID: id, Lang: "binary", SourceSHA256: strings.Repeat("a", 64),
			Binaries: []model.Binary{{Name: "main", DataB64: "eA==", Mode: "exec", SHA256: hex.EncodeToString(hash[:])}},
		})
	}
	return req
}

func TestValidateCommunicationV2BindsSeatsAndArtifacts(t *testing.T) {
	if err := Validate(validCommunicationV2Request()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*model.RunRequest)
		want string
	}{
		{"singular id", func(r *model.RunRequest) { r.Communication.ParticipantProgramID = "seat0" }, "exactly two"},
		{"duplicate seats", func(r *model.RunRequest) { r.Communication.ParticipantProgramIDs[1] = "seat0" }, "must differ"},
		{"manager as seat", func(r *model.RunRequest) { r.Communication.ParticipantProgramIDs[1] = "manager" }, "must differ"},
		{"seat whitespace", func(r *model.RunRequest) { r.Communication.ParticipantProgramIDs[1] = " seat1" }, "surrounding whitespace"},
		{"more seats", func(r *model.RunRequest) {
			r.Communication.ParticipantProgramIDs = append(r.Communication.ParticipantProgramIDs, "seat2")
		}, "exactly two"},
		{"count mismatch", func(r *model.RunRequest) { r.Communication.ParticipantCount = 3 }, "participant_count 2"},
		{"old result", func(r *model.RunRequest) { r.Communication.ResultProtocol = "manager-result-v1" }, "match-result-v1"},
		{"seed", func(r *model.RunRequest) { r.Communication.MatchSeed = strings.Repeat("A", 64) }, "match_seed"},
		{"input binding", func(r *model.RunRequest) { r.Communication.InputSHA256 = "" }, "input_sha256"},
		{"runtime binding", func(r *model.RunRequest) { r.Communication.RuntimeFingerprint = "" }, "runtime_fingerprint"},
		{"source binding", func(r *model.RunRequest) { r.Programs[1].SourceSHA256 = "" }, "source_sha256"},
		{"artifact binding", func(r *model.RunRequest) { r.Programs[1].Binaries[0].SHA256 = strings.Repeat("0", 64) }, "does not match"},
		{"missing digest", func(r *model.RunRequest) { r.Programs[1].Binaries[0].SHA256 = "" }, "inline binaries with sha256"},
		{"remote artifact", func(r *model.RunRequest) {
			r.Programs[1].Binaries[0].DataB64 = ""
			r.Programs[1].Binaries[0].DataURL = "https://example.invalid/artifact"
		}, "inline binaries with sha256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validCommunicationV2Request()
			tc.edit(req)
			if err := Validate(req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateCommunicationV1RejectsV2Fields(t *testing.T) {
	req := validCommunicationRequest()
	req.Communication.ParticipantProgramIDs = []string{"participant", "participant"}
	if err := Validate(req); err == nil {
		t.Fatal("v1 must not silently ignore explicit seat programs")
	}
}
