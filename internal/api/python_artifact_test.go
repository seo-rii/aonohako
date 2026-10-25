package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/pythonpolicy"
)

func TestCompilePythonArtifactMode(t *testing.T) {
	for _, tc := range []struct {
		lang  string
		mode  pythonpolicy.ArtifactMode
		valid bool
	}{
		{lang: "PYTHON3", valid: true},
		{lang: "python", mode: pythonpolicy.ArtifactModeBytecodeV1, valid: true},
		{lang: "PYPY3", mode: pythonpolicy.ArtifactModeBytecodeV1, valid: true},
		{lang: "pypy", mode: pythonpolicy.ArtifactModeBytecodeV1, valid: true},
		{lang: "PYTHON3", mode: "bytecode-v2"},
		{lang: "PYTHON3", mode: " BYTECODE-V1 "},
		{lang: "C", mode: pythonpolicy.ArtifactModeBytecodeV1},
		{lang: "CYTHON", mode: pythonpolicy.ArtifactModeBytecodeV1},
	} {
		t.Run(tc.lang+"/"+string(tc.mode), func(t *testing.T) {
			called := false
			server := NewWithServices(configForTest(t), compileRunnerStub{run: func(_ context.Context, req *model.CompileRequest) model.CompileResponse {
				called = true
				if req.PythonArtifactMode != tc.mode {
					t.Fatalf("runner mode = %q, want %q", req.PythonArtifactMode, tc.mode)
				}
				return model.CompileResponse{Status: model.CompileStatusOK}
			}}, nil)
			body, err := json.Marshal(model.CompileRequest{
				Lang:               tc.lang,
				PythonArtifactMode: tc.mode,
				Sources:            []model.Source{{Name: "Main.py", DataB64: "cHJpbnQoMSkK"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			testServer := httptest.NewServer(server.Handler())
			defer testServer.Close()
			response, err := http.Post(testServer.URL+"/compile", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			responseBody, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if tc.valid {
				if response.StatusCode != http.StatusOK || !called {
					t.Fatalf("status=%d called=%t body=%s", response.StatusCode, called, responseBody)
				}
			} else if response.StatusCode != http.StatusBadRequest || called || !strings.Contains(string(responseBody), "invalid_python_artifact_mode") {
				t.Fatalf("status=%d called=%t body=%s", response.StatusCode, called, responseBody)
			}
		})
	}
}
