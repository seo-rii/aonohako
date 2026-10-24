package compile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aonohako/internal/compilewire"
	"aonohako/internal/model"
)

func TestRemoteCompileRejectsMalformedAndOversizedArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		response   func() model.CompileResponse
	}{
		{"malformed JSON", "remote result decode failed", nil},
		{"artifact count", "too many artifacts", func() model.CompileResponse {
			return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: make([]model.Artifact, compilewire.ArtifactCount+1)}
		}},
		{"artifact name", "name: max", func() model.CompileResponse {
			return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{{Name: strings.Repeat("a", compilewire.ArtifactNameBytes+1)}}}
		}},
		{"reason code", "reason_code too long", func() model.CompileResponse {
			return model.CompileResponse{Status: model.CompileStatusOK, ReasonCode: strings.Repeat("a", compilewire.ReasonCodeBytes+1)}
		}},
		{"one artifact", "artifact too large", func() model.CompileResponse {
			data := base64.StdEncoding.EncodeToString(make([]byte, compilewire.ArtifactBytes+1))
			return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{{Name: "a", DataB64: data}}}
		}},
		{"artifact total", "artifact total size exceeded", func() model.CompileResponse {
			data := base64.StdEncoding.EncodeToString(make([]byte, compilewire.ArtifactBytes))
			return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{
				{Name: "a", DataB64: data}, {Name: "b", DataB64: data}, {Name: "c", DataB64: data}, {Name: "d", DataB64: "AA=="},
			}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: result\ndata: ")
				if tc.response == nil {
					_, _ = io.WriteString(w, "{\n\n")
					return
				}
				if err := json.NewEncoder(w).Encode(tc.response()); err == nil {
					_, _ = io.WriteString(w, "\n")
				}
			}))
			defer remote.Close()
			runner := &remoteRunner{client: remote.Client(), compileURL: remote.URL, idleTimeout: -1}
			got := runner.Run(context.Background(), &model.CompileRequest{Lang: "TEXT"})
			if got.Status != model.CompileStatusInternal || !strings.Contains(got.Reason, tc.want) {
				t.Fatalf("remote rejection: status=%s reason=%s", got.Status, got.Reason)
			}
		})
	}
}
