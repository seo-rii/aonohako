package compile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/compilewire"
	"aonohako/internal/model"
)

func TestPrepareCompileResponseRejectsCombinedArtifactCollections(t *testing.T) {
	// ReScript collects its entry point separately from generated modules. Each
	// collection fits its own limit, but their final response exceeds 512 files.
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.res"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK, Stdout: "compiler output"},
		hook: func(workDir, _ string, _, _ []string) {
			output := filepath.Join(workDir, "lib", "js")
			if err := os.MkdirAll(output, 0o700); err != nil {
				t.Fatal(err)
			}
			for i := 0; i <= compilewire.ArtifactCount; i++ {
				name := fmt.Sprintf("Module%03d.js", i)
				if i == 0 {
					name = "Main.js"
				}
				if err := os.WriteFile(filepath.Join(output, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	raw := (reScriptCompiler{}).Compile(context.Background(), CompileJob{
		WorkDir: workDir, Target: "Main.js", Runner: runner,
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.res"}}},
	})
	if raw.Status != model.CompileStatusOK || len(raw.Artifacts) != compilewire.ArtifactCount+1 {
		t.Fatalf("combined compiler response: status=%s artifacts=%d reason=%s", raw.Status, len(raw.Artifacts), raw.Reason)
	}
	wire, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("combined compiler response before boundary validation: artifacts=%d decoded_bytes=0 json_bytes=%d", len(raw.Artifacts), len(wire))
	client := &http.Client{Transport: compileRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader("event: result\ndata: " + string(wire) + "\n\n"))}, nil
	})}
	remote := &remoteRunner{client: client, compileURL: "http://compiler.invalid/compile", idleTimeout: -1}
	remoteResult := remote.Run(context.Background(), &model.CompileRequest{Lang: "TEXT"})
	if remoteResult.Status != model.CompileStatusInternal || !strings.Contains(remoteResult.Reason, "too many artifacts") {
		t.Fatalf("remote accepted combined raw response: status=%s reason=%s", remoteResult.Status, remoteResult.Reason)
	}
	got := PrepareResponse(raw)
	if got.Status != model.CompileStatusInternal || len(got.Artifacts) != 0 || !strings.Contains(got.Reason, "too many artifacts") || got.Stdout != raw.Stdout {
		t.Fatalf("combined response was not rejected: status=%s artifacts=%d reason=%s", got.Status, len(got.Artifacts), got.Reason)
	}
}

func TestPrepareCompileResponseRejectsAggregateArtifactBytes(t *testing.T) {
	maximum := base64.StdEncoding.EncodeToString(make([]byte, compilewire.ArtifactBytes))
	response := model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{
		{Name: "a", DataB64: maximum}, {Name: "b", DataB64: maximum}, {Name: "c", DataB64: maximum}, {Name: "d", DataB64: "AA=="},
	}}
	got := PrepareResponse(response)
	if got.Status != model.CompileStatusInternal || len(got.Artifacts) != 0 || !strings.Contains(got.Reason, "artifact total size exceeded") {
		t.Fatalf("aggregate response was not rejected: status=%s artifacts=%d reason=%s", got.Status, len(got.Artifacts), got.Reason)
	}
	response.Artifacts[3].DataB64 = ""
	response.Artifacts = append(response.Artifacts, make([]model.Artifact, compilewire.ArtifactCount-len(response.Artifacts))...)
	for i := range response.Artifacts {
		response.Artifacts[i].Name = fmt.Sprintf("%03d%s", i, strings.Repeat("x", compilewire.ArtifactNameBytes-3))
	}
	got = PrepareResponse(response)
	if got.Status != model.CompileStatusOK || len(got.Artifacts) != compilewire.ArtifactCount {
		t.Fatalf("maximum valid artifacts changed: status=%s artifacts=%d reason=%s", got.Status, len(got.Artifacts), got.Reason)
	}
	for i := range response.Artifacts {
		if got.Artifacts[i] != response.Artifacts[i] {
			t.Fatalf("maximum valid artifact %d changed", i)
		}
	}
}

func TestPrepareCompileResponseRejectsInvalidArtifactEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		artifact   model.Artifact
	}{
		{"name", "artifact name too long", model.Artifact{Name: strings.Repeat("a", compilewire.ArtifactNameBytes+1)}},
		{"data", "invalid base64", model.Artifact{Name: "a", DataB64: "not base64"}},
		{"size", "artifact too large", model.Artifact{Name: "a", DataB64: base64.StdEncoding.EncodeToString(make([]byte, compilewire.ArtifactBytes+1))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := PrepareResponse(model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{tc.artifact}})
			if got.Status != model.CompileStatusInternal || len(got.Artifacts) != 0 || !strings.Contains(got.Reason, tc.want) {
				t.Fatalf("invalid response was not rejected: status=%s artifacts=%d reason=%s", got.Status, len(got.Artifacts), got.Reason)
			}
		})
	}
}

func TestPrepareCompileResponseBoundsJSONEscapedArtifactData(t *testing.T) {
	// Share one modest string across files: only 257 decoded bytes, but the
	// CR/LF pairs expand to over 64 MiB when the final response is JSON encoded.
	data := strings.Repeat("\r\n", 64<<10) + "AA=="
	response := model.CompileResponse{Status: model.CompileStatusOK, Artifacts: make([]model.Artifact, 257)}
	for i := range response.Artifacts {
		response.Artifacts[i] = model.Artifact{Name: fmt.Sprintf("file-%03d", i), DataB64: data}
	}
	got := PrepareResponse(response)
	if got.Status != model.CompileStatusInternal || len(got.Artifacts) != 0 || !strings.Contains(got.Reason, "encoded artifact total size exceeded") {
		t.Fatalf("JSON-amplified artifacts were not rejected: status=%s artifacts=%d reason=%s", got.Status, len(got.Artifacts), got.Reason)
	}
	response.Artifacts = []model.Artifact{{Name: "wrapped", DataB64: "Y\r\nQ==\r\n"}}
	got = PrepareResponse(response)
	if got.Status != model.CompileStatusOK || len(got.Artifacts) != 1 || got.Artifacts[0] != response.Artifacts[0] {
		t.Fatalf("ordinary wrapped Base64 changed: status=%s artifacts=%v reason=%s", got.Status, got.Artifacts, got.Reason)
	}
}
