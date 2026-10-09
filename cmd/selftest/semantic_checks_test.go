package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestCompileExecuteRejectsBrokenVerification(t *testing.T) {
	for _, mode := range []string{"old-inputs-only", "always-accepted", "always-accepted-text", "empty-artifacts"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				switch r.URL.Path {
				case "/compile":
					var request model.CompileRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode compile request: %v", err)
						return
					}
					result := model.CompileResponse{Status: model.CompileStatusOK}
					if mode != "empty-artifacts" {
						for _, source := range request.Sources {
							result.Artifacts = append(result.Artifacts, model.Artifact{Name: source.Name, DataB64: source.DataB64})
						}
					}
					payload, _ := json.Marshal(result)
					fmt.Fprintf(w, "event: result\ndata: %s\n\n", payload)
				case "/execute":
					var request model.RunRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode execute request: %v", err)
						return
					}
					var a, b int
					fmt.Sscanf(request.Stdin, "%d %d", &a, &b)
					output := fmt.Sprintf("%d\n", a+b)
					if mode == "old-inputs-only" && request.Stdin != "20 22\n" && request.Stdin != "7 13\n" {
						output = request.Stdin
					}
					status := model.RunStatusAccepted
					if mode != "always-accepted" && !(mode == "always-accepted-text" && request.Lang == "text") && output != request.ExpectedStdout {
						status = model.RunStatusWA
					}
					payload, _ := json.Marshal(model.RunResponse{Status: status})
					fmt.Fprintf(w, "event: result\ndata: %s\n\n", payload)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			language := "befunge"
			if mode == "always-accepted-text" {
				language = "plain"
			}
			err := verifyCompileExecuteLanguages(server.URL, language)
			if err == nil {
				t.Fatalf("verification accepted %s regression", mode)
			}
			want := map[string]string{
				"old-inputs-only":      "execute case 3/",
				"always-accepted":      "wrong-answer control returned",
				"always-accepted-text": "text artifact is printed verbatim wrong-answer control returned",
				"empty-artifacts":      "compile succeeded without artifacts",
			}[mode]
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("rejected for %q, want %q", err, want)
			}
		})
	}
}

func TestSemanticChecksRejectUnconditionalSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		var result any
		if r.URL.Path == "/compile" {
			result = model.CompileResponse{
				Status:    model.CompileStatusOK,
				Artifacts: []model.Artifact{{Name: "Main", DataB64: encodeScript("ignored")}},
			}
		} else {
			result = model.RunResponse{Status: model.RunStatusAccepted}
		}
		payload, _ := json.Marshal(result)
		fmt.Fprintf(w, "event: result\ndata: %s\n\n", payload)
	}))
	defer server.Close()
	for _, tc := range []languageSemanticCase{
		{name: "invalid proof", expectedCompileStatus: model.CompileStatusCompileError},
		{name: "runtime error", expectedRunStatus: model.RunStatusRE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyLanguageSemanticCase(server.URL, "golfscript", "GOLFSCRIPT", compileExecuteCases()["golfscript"], tc)
			if err == nil {
				t.Fatalf("semantic verification accepted %s", tc.name)
			}
		})
	}
}
