package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"aonohako/internal/model"
)

func TestVerifyTLAModelCheckingRequiresValidModelAndCounterexample(t *testing.T) {
	for _, compileLanguage := range []string{"TLA", "TLAPLUS"} {
		for _, tc := range []struct {
			name           string
			validStatus    string
			invalidStatus  string
			invalidStderr  string
			wantError      string
			wantExecutions int
		}{
			{
				name: "valid model and false invariant", validStatus: model.RunStatusAccepted,
				invalidStatus: model.RunStatusRE, invalidStderr: "Error: Invariant InitialOnly is violated.\n",
				wantExecutions: 2,
			},
			{
				name: "lost counterexample exit status", validStatus: model.RunStatusAccepted,
				invalidStatus: model.RunStatusAccepted, invalidStderr: "Error: Invariant InitialOnly is violated.\n",
				wantError: "invariant InitialOnly execute failed", wantExecutions: 2,
			},
			{
				name: "sandbox initialization failure", validStatus: model.RunStatusRE,
				invalidStatus: model.RunStatusRE,
				wantError:     "invariant Safe execute failed", wantExecutions: 1,
			},
			{
				name: "unrelated error instead of counterexample", validStatus: model.RunStatusAccepted,
				invalidStatus: model.RunStatusRE, invalidStderr: "Error: TLC failed to initialize.\n",
				wantError: "failed without a TLC counterexample", wantExecutions: 2,
			},
		} {
			t.Run(compileLanguage+"/"+tc.name, func(t *testing.T) {
				compiles, executions := 0, 0
				var mu sync.Mutex
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					var response any
					switch r.URL.Path {
					case "/compile":
						var request model.CompileRequest
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							t.Errorf("decode compile request: %v", err)
							http.Error(w, "invalid request", http.StatusBadRequest)
							return
						}
						if request.Lang != compileLanguage {
							t.Errorf("compile language = %q, want %q", request.Lang, compileLanguage)
						}
						var artifacts []model.Artifact
						for _, source := range request.Sources {
							artifacts = append(artifacts, model.Artifact{Name: source.Name, DataB64: source.DataB64})
						}
						response = model.CompileResponse{Status: model.CompileStatusOK, Artifacts: artifacts}
						compiles++
					case "/execute":
						var request model.RunRequest
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
							t.Errorf("decode execute request: %v", err)
							http.Error(w, "invalid request", http.StatusBadRequest)
							return
						}
						if request.Lang != "tla" {
							t.Errorf("run language = %q, want tla", request.Lang)
						}
						files := map[string]string{}
						for _, binary := range request.Binaries {
							data, err := base64.StdEncoding.DecodeString(binary.DataB64)
							if err != nil {
								t.Errorf("decode binary %s: %v", binary.Name, err)
							}
							files[binary.Name] = string(data)
						}
						for _, required := range []string{"Init == x = 0", "Next == x' = IF x = 0 THEN 1 ELSE x", "Safe == x \\in {0, 1}", "InitialOnly == x = 0"} {
							if !strings.Contains(files["Main.tla"], required) {
								t.Errorf("specification lacks two-state model condition %q", required)
							}
						}
						wantInvariant := "Safe"
						status, stderr := tc.validStatus, ""
						if executions == 1 {
							wantInvariant = "InitialOnly"
							status, stderr = tc.invalidStatus, tc.invalidStderr
						}
						if want := "SPECIFICATION Spec\nINVARIANT " + wantInvariant + "\n"; files["Main.cfg"] != want {
							t.Errorf("configuration = %q, want %q", files["Main.cfg"], want)
						}
						response = model.RunResponse{Status: status, Stderr: stderr}
						executions++
					default:
						t.Errorf("unexpected request path %s", r.URL.Path)
						http.NotFound(w, r)
						return
					}
					payload, err := json.Marshal(response)
					if err != nil {
						t.Errorf("encode response: %v", err)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: result\ndata: %s\n\n", payload)
				}))
				defer server.Close()
				err := verifyTLAModelChecking(server.URL, compileLanguage)
				if tc.wantError == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("regression error = %v, want containing %q", err, tc.wantError)
				}
				mu.Lock()
				defer mu.Unlock()
				if compiles != tc.wantExecutions || executions != tc.wantExecutions {
					t.Errorf("compile/execute requests = %d/%d, want %d/%d", compiles, executions, tc.wantExecutions, tc.wantExecutions)
				}
			})
		}
	}
}
